package toc

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/mk6i/retro-aim-server/state"
	"github.com/mk6i/retro-aim-server/wire"
)

var (
	// capChat is the UUID that represents an OSCAR client's ability to chat
	capChat = uuid.MustParse("748F2420-6287-11D1-8222-444553540000")
)

// NewChatRegistry creates a new ChatRegistry instances.
func NewChatRegistry() *ChatRegistry {
	chatRegistry := &ChatRegistry{
		lookup:   make(map[int]wire.ICBMRoomInfo),
		sessions: make(map[int]*state.Session),
		m:        sync.RWMutex{},
	}
	return chatRegistry
}

// ChatRegistry manages the chat rooms that a user is connected to during a TOC
// session. It maintains mappings between chat room identifiers, metadata, and
// active chat sessions.
//
// This struct provides thread-safe operations for adding, retrieving, and managing
// chat room metadata and associated sessions.
type ChatRegistry struct {
	lookup   map[int]wire.ICBMRoomInfo // Maps chat room IDs to their metadata.
	sessions map[int]*state.Session    // Tracks active chat sessions by chat room ID.
	nextID   int                       // Incremental identifier for newly added chat rooms.
	m        sync.RWMutex              // Synchronization primitive for concurrent access.
}

// Add registers metadata for a newly joined chat room and returns a unique
// identifier for it. If the room is already registered, it returns the existing ID.
func (c *ChatRegistry) Add(room wire.ICBMRoomInfo) int {
	c.m.Lock()
	defer c.m.Unlock()
	for chatID, r := range c.lookup {
		if r == room {
			return chatID
		}
	}
	id := c.nextID
	c.lookup[id] = room
	c.nextID++
	return id
}

// LookupRoom retrieves metadata for the chat room registered with chatID.
// It returns the room metadata and a boolean indicating whether the chat ID
// was found.
func (c *ChatRegistry) LookupRoom(chatID int) (wire.ICBMRoomInfo, bool) {
	c.m.RLock()
	defer c.m.RUnlock()
	room, found := c.lookup[chatID]
	return room, found
}

// RegisterSess associates a chat session with a chat room. If a session is
// already registered for the given chat ID, it will be overwritten.
func (c *ChatRegistry) RegisterSess(chatID int, sess *state.Session) {
	c.m.Lock()
	defer c.m.Unlock()
	c.sessions[chatID] = sess
}

// RetrieveSess retrieves the chat session associated with the given chat ID.
// If no session is registered for the chat ID, it returns nil.
func (c *ChatRegistry) RetrieveSess(chatID int) *state.Session {
	c.m.RLock()
	defer c.m.RUnlock()
	return c.sessions[chatID]
}

// OSCARProxy acts as a bridge between TOC clients and the OSCAR server,
// translating protocol messages between the two.
//
// It performs the following functions:
//   - Receives TOC messages from the client, converts them into SNAC messages,
//     and forwards them to the OSCAR server. The SNAC response is then converted
//     back into a TOC response for the client.
//   - Receives incoming messages from the OSCAR server and translates them into
//     TOC responses for the client.
type OSCARProxy struct {
	AuthService         AuthService
	BuddyListRegistry   BuddyListRegistry
	BuddyService        BuddyService
	ChatNavService      ChatNavService
	ChatService         ChatService
	CookieBaker         CookieBaker
	DirSearchService    DirSearchService
	ICBMService         ICBMService
	LocateService       LocateService
	Logger              *slog.Logger
	OServiceServiceBOS  OServiceService
	OServiceServiceChat OServiceService
	PermitDenyService   PermitDenyService
	TOCConfigStore      TOCConfigStore
}

// RecvClientCmd processes a client TOC command and returns a server reply.
//
// * sessBOS is the current user's session.
// * chatRegistry manages the current user's chat sessions
// * payload is the command + arguments
// * toCh is the channel that transports messages to client
// * doAsync performs async tasks, is auto-cleaned up by caller
//
// It returns true if the server can continue processing commands.
func (s OSCARProxy) RecvClientCmd(
	ctx context.Context,
	sessBOS *state.Session,
	chatRegistry *ChatRegistry,
	payload []byte,
	toCh chan<- []byte,
	doAsync func(f func() error),
) (reply string, ok bool) {
	cmd := payload
	if idx := bytes.IndexByte(payload, ' '); idx > -1 {
		cmd = cmd[:idx]
	}

	if s.Logger.Enabled(ctx, slog.LevelDebug) {
		s.Logger.DebugContext(ctx, "client request", "command", payload)
	} else {
		s.Logger.InfoContext(ctx, "client request", "command", cmd)
	}

	switch string(cmd) {
	case "toc_send_im":
		return s.SendIM(ctx, sessBOS, payload), true
	case "toc_init_done":
		return s.InitDone(ctx, sessBOS, payload), true
	case "toc_add_buddy":
		return s.AddBuddy(ctx, sessBOS, payload), true
	case "toc_remove_buddy":
		return s.RemoveBuddy(ctx, sessBOS, payload), true
	case "toc_add_permit":
		return s.AddPermit(ctx, sessBOS, payload), true
	case "toc_add_deny":
		return s.AddDeny(ctx, sessBOS, payload), true
	case "toc_set_away":
		return s.SetAway(ctx, sessBOS, payload), true
	case "toc_set_caps":
		return s.SetCaps(ctx, sessBOS, payload), true
	case "toc_evil":
		return s.Evil(ctx, sessBOS, payload), true
	case "toc_get_info":
		return s.GetInfoURL(ctx, sessBOS, payload), true
	case "toc_chat_join", "toc_chat_accept":
		var chatID int
		var msg string

		if string(cmd) == "toc_chat_join" {
			chatID, msg = s.ChatJoin(ctx, sessBOS, chatRegistry, payload)
		} else {
			chatID, msg = s.ChatAccept(ctx, sessBOS, chatRegistry, payload)
		}

		if msg == cmdInternalSvcErr {
			// todo idk if this is worth cancelling the connection over
			return "", false
		}

		doAsync(func() error {
			sess := chatRegistry.RetrieveSess(chatID)
			s.RecvChat(ctx, sess, chatID, toCh)
			return nil
		})

		return msg, true
	case "toc_chat_send":
		return s.ChatSend(ctx, chatRegistry, payload), true
	case "toc_chat_leave":
		return s.ChatLeave(ctx, chatRegistry, payload), true
	case "toc_set_info":
		return s.SetInfo(ctx, sessBOS, payload), true
	case "toc_set_dir":
		return s.SetDir(ctx, sessBOS, payload), true
	case "toc_set_idle":
		return s.SetIdle(ctx, sessBOS, payload), true
	case "toc_set_config":
		return s.SetConfig(ctx, sessBOS, payload), true
	case "toc_chat_invite":
		return s.ChatInvite(ctx, sessBOS, chatRegistry, payload), true
	case "toc_dir_search":
		return s.GetDirSearchURL(ctx, sessBOS, payload), true
	case "toc_get_dir":
		return s.GetDirURL(ctx, sessBOS, payload), true
	}

	s.Logger.ErrorContext(ctx, fmt.Sprintf("unsupported TOC command %s", cmd))
	return "", true
}

// AddBuddy handles the toc_add_buddy TOC command.
//
// From the TiK documentation:
//
//	Add buddies to your buddy list. This does not change your saved config.
//
// Command syntax: toc_add_buddy <Buddy User 1> [<Buddy User2> [<Buddy User 3> [...]]]
func (s OSCARProxy) AddBuddy(ctx context.Context, me *state.Session, cmd []byte) string {
	users, err := parseArgs(cmd, "toc_add_buddy")
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	snac := wire.SNAC_0x03_0x04_BuddyAddBuddies{}
	for _, sn := range users {
		snac.Buddies = append(snac.Buddies, struct {
			ScreenName string `oscar:"len_prefix=uint8"`
		}{ScreenName: sn})
	}

	if err := s.BuddyService.AddBuddies(ctx, me, snac); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("BuddyService.AddBuddies: %w", err))
	}

	return ""
}

// AddPermit handles the toc_add_permit TOC command.
//
// From the TiK documentation:
//
//	ADD the following people to your permit mode. If you are in deny mode it
//	will switch you to permit mode first. With no arguments and in deny mode
//	this will switch you to permit none. If already in permit mode, no
//	arguments does nothing and your permit list remains the same.
//
// Command syntax: toc_add_permit [ <User 1> [<User 2> [...]]]
func (s OSCARProxy) AddPermit(ctx context.Context, me *state.Session, cmd []byte) string {
	users, err := parseArgs(cmd, "toc_add_permit")
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	snac := wire.SNAC_0x09_0x05_PermitDenyAddPermListEntries{}
	for _, sn := range users {
		snac.Users = append(snac.Users, struct {
			ScreenName string `oscar:"len_prefix=uint8"`
		}{ScreenName: sn})
	}

	if err := s.PermitDenyService.AddPermListEntries(ctx, me, snac); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("PermitDenyService.AddPermListEntries: %w", err))
	}
	return ""
}

// AddDeny handles the toc_add_deny TOC command.
//
// From the TiK documentation:
//
//	ADD the following people to your deny mode. If you are in permit mode it
//	will switch you to deny mode first. With no arguments and in permit mode,
//	this will switch you to deny none. If already in deny mode, no arguments
//	does nothing and your deny list remains unchanged.
//
// Command syntax: toc_add_deny [ <User 1> [<User 2> [...]]]
func (s OSCARProxy) AddDeny(ctx context.Context, me *state.Session, cmd []byte) string {
	users, err := parseArgs(cmd, "toc_add_deny")
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	snac := wire.SNAC_0x09_0x07_PermitDenyAddDenyListEntries{}
	for _, sn := range users {
		snac.Users = append(snac.Users, struct {
			ScreenName string `oscar:"len_prefix=uint8"`
		}{ScreenName: sn})
	}

	if err := s.PermitDenyService.AddDenyListEntries(ctx, me, snac); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("PermitDenyService.AddDenyListEntries: %w", err))
	}
	return ""
}

// ChatAccept handles the toc_chat_accept TOC command.
//
// From the TiK documentation:
//
//	Accept a CHAT_INVITE message from TOC. The server will send a CHAT_JOIN in
//	response.
//
// Command syntax: toc_chat_accept <Chat Room ID>
func (s OSCARProxy) ChatAccept(
	ctx context.Context,
	me *state.Session,
	chatRegistry *ChatRegistry,
	cmd []byte,
) (int, string) {
	var chatIDStr string

	if _, err := parseArgs(cmd, "toc_chat_accept", &chatIDStr); err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	chatID, err := strconv.Atoi(chatIDStr)
	if err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("strconv.Atoi: %w", err))
	}
	chatInfo, found := chatRegistry.LookupRoom(chatID)
	if !found {
		return 0, s.runtimeErr(ctx, fmt.Errorf("chatRegistry.LookupRoom: no chat found for ID %d", chatID))
	}

	reqRoomSNAC := wire.SNAC_0x0D_0x04_ChatNavRequestRoomInfo{
		Cookie:         chatInfo.Cookie,
		Exchange:       chatInfo.Exchange,
		InstanceNumber: chatInfo.Instance,
	}
	reqRoomReply, err := s.ChatNavService.RequestRoomInfo(ctx, wire.SNACFrame{}, reqRoomSNAC)
	if err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("ChatNavService.RequestRoomInfo: %w", err))
	}

	reqRoomReplyBody, ok := reqRoomReply.Body.(wire.SNAC_0x0D_0x09_ChatNavNavInfo)
	if !ok {
		return 0, s.runtimeErr(ctx, fmt.Errorf("chatNavService.RequestRoomInfo: unexpected response type %v", reqRoomReplyBody))
	}
	b, hasInfo := reqRoomReplyBody.Bytes(wire.ChatNavTLVRoomInfo)
	if !hasInfo {
		return 0, s.runtimeErr(ctx, errors.New("reqRoomReplyBody.Bytes: missing wire.ChatNavTLVRoomInfo"))
	}

	roomInfo := wire.SNAC_0x0E_0x02_ChatRoomInfoUpdate{}
	if err := wire.UnmarshalBE(&roomInfo, bytes.NewReader(b)); err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("wire.UnmarshalBE: %w", err))
	}

	roomName, hasName := roomInfo.Bytes(wire.ChatRoomTLVRoomName)
	if !hasName {
		return 0, s.runtimeErr(ctx, errors.New("roomInfo.Bytes: missing wire.ChatRoomTLVRoomName"))
	}

	svcReqSNAC := wire.SNAC_0x01_0x04_OServiceServiceRequest{
		FoodGroup: wire.Chat,
		TLVRestBlock: wire.TLVRestBlock{
			TLVList: wire.TLVList{
				wire.NewTLVBE(0x01, wire.SNAC_0x01_0x04_TLVRoomInfo{
					Cookie: chatInfo.Cookie,
				}),
			},
		},
	}
	svcReqReply, err := s.OServiceServiceBOS.ServiceRequest(ctx, me, wire.SNACFrame{}, svcReqSNAC)
	if err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("OServiceServiceBOS.ServiceRequest: %w", err))
	}

	svcReqReplyBody, ok := svcReqReply.Body.(wire.SNAC_0x01_0x05_OServiceServiceResponse)
	if !ok {
		return 0, s.runtimeErr(ctx, fmt.Errorf("OServiceServiceBOS.ServiceRequest: unexpected response type %v", svcReqReplyBody))
	}

	loginCookie, hasCookie := svcReqReplyBody.Bytes(wire.OServiceTLVTagsLoginCookie)
	if !hasCookie {
		return 0, s.runtimeErr(ctx, errors.New("missing wire.OServiceTLVTagsLoginCookie"))
	}

	chatSess, err := s.AuthService.RegisterChatSession(ctx, loginCookie)
	if err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("AuthService.RegisterChatSession: %w", err))
	}

	chatRegistry.RegisterSess(chatID, chatSess)

	if err := s.OServiceServiceChat.ClientOnline(ctx, wire.SNAC_0x01_0x02_OServiceClientOnline{}, chatSess); err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("OServiceServiceChat.ClientOnline: %w", err))
	}

	return chatID, fmt.Sprintf("CHAT_JOIN:%d:%s", chatID, roomName)
}

// ChatInvite handles the toc_chat_invite TOC command.
//
// From the TiK documentation:
//
//	Once you are inside a chat room you can invite other people into that room.
//	Remember to quote and encode the invite message.
//
// Command syntax: toc_chat_invite <Chat Room ID> <Invite Msg> <buddy1> [<buddy2> [<buddy3> [...]]]
func (s OSCARProxy) ChatInvite(ctx context.Context, me *state.Session, chatRegistry *ChatRegistry, cmd []byte) string {
	var chatRoomIDStr, msg string

	users, err := parseArgs(cmd, "toc_chat_invite", &chatRoomIDStr, &msg)
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	chatID, err := strconv.Atoi(chatRoomIDStr)
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("strconv.Atoi: %w", err))
	}

	roomInfo, found := chatRegistry.LookupRoom(chatID)
	if !found {
		return s.runtimeErr(ctx, fmt.Errorf("chatRegistry.LookupRoom: chat ID `%d` not found", chatID))
	}

	for _, guest := range users {
		snac := wire.SNAC_0x04_0x06_ICBMChannelMsgToHost{
			ChannelID:  wire.ICBMChannelRendezvous,
			ScreenName: guest,
			TLVRestBlock: wire.TLVRestBlock{
				TLVList: wire.TLVList{
					wire.NewTLVBE(0x05, wire.ICBMCh2Fragment{
						Type:       0,
						Capability: capChat,
						TLVRestBlock: wire.TLVRestBlock{
							TLVList: wire.TLVList{
								wire.NewTLVBE(10, uint16(1)),
								wire.NewTLVBE(12, msg),
								wire.NewTLVBE(13, "us-ascii"),
								wire.NewTLVBE(14, "en"),
								wire.NewTLVBE(10001, roomInfo),
							},
						},
					}),
				},
			},
		}

		if _, err := s.ICBMService.ChannelMsgToHost(ctx, me, wire.SNACFrame{}, snac); err != nil {
			return s.runtimeErr(ctx, fmt.Errorf("ICBMService.ChannelMsgToHost: %w", err))
		}
	}

	return ""
}

// ChatJoin handles the toc_chat_join TOC command.
//
// From the TiK documentation:
//
//	Join a chat room in the given exchange. Exchange is an integer that
//	represents a group of chat rooms. Different exchanges have different
//	properties. For example some exchanges might have room replication (ie a
//	room never fills up, there are just multiple instances.) and some exchanges
//	might have navigational information. Currently, exchange should always be
//	4, however this may change in the future. You will either receive an ERROR
//	if the room couldn't be joined or a CHAT_JOIN message. The Chat Room Name
//	is case-insensitive and consecutive spaces are removed.
//
// Command syntax: toc_chat_join <Exchange> <Chat Room Name>
func (s OSCARProxy) ChatJoin(
	ctx context.Context,
	me *state.Session,
	chatRegistry *ChatRegistry,
	cmd []byte,
) (int, string) {
	var exchangeStr, roomName string

	if _, err := parseArgs(cmd, "toc_chat_join", &exchangeStr, &roomName); err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	// create room or retrieve the room if it already exists
	exchange, err := strconv.Atoi(exchangeStr)
	if err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("strconv.Atoi: %w", err))
	}

	mkRoomReq := wire.SNAC_0x0E_0x02_ChatRoomInfoUpdate{
		Exchange: uint16(exchange),
		Cookie:   "create",
		TLVBlock: wire.TLVBlock{
			TLVList: wire.TLVList{
				wire.NewTLVBE(wire.ChatRoomTLVRoomName, roomName),
			},
		},
	}
	mkRoomReply, err := s.ChatNavService.CreateRoom(ctx, me, wire.SNACFrame{}, mkRoomReq)
	if err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("ChatNavService.CreateRoom: %w", err))
	}

	mkRoomReplyBody, ok := mkRoomReply.Body.(wire.SNAC_0x0D_0x09_ChatNavNavInfo)
	if !ok {
		return 0, s.runtimeErr(ctx, fmt.Errorf("chatNavService.CreateRoom: unexpected response type %v", mkRoomReplyBody))
	}
	buf, ok := mkRoomReplyBody.Bytes(wire.ChatNavTLVRoomInfo)
	if !ok {
		return 0, s.runtimeErr(ctx, errors.New("mkRoomReplyBody.Bytes: missing wire.ChatNavTLVRoomInfo"))
	}

	inBody := wire.SNAC_0x0E_0x02_ChatRoomInfoUpdate{}
	if err := wire.UnmarshalBE(&inBody, bytes.NewReader(buf)); err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("wire.UnmarshalBE: %w", err))
	}

	svcReqSNAC := wire.SNAC_0x01_0x04_OServiceServiceRequest{
		FoodGroup: wire.Chat,
		TLVRestBlock: wire.TLVRestBlock{
			TLVList: wire.TLVList{
				wire.NewTLVBE(0x01, wire.SNAC_0x01_0x04_TLVRoomInfo{
					Cookie: inBody.Cookie,
				}),
			},
		},
	}
	svcReqReply, err := s.OServiceServiceBOS.ServiceRequest(ctx, me, wire.SNACFrame{}, svcReqSNAC)
	if err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("OServiceServiceBOS.ServiceRequest: %w", err))
	}

	svcReqReplyBody, ok := svcReqReply.Body.(wire.SNAC_0x01_0x05_OServiceServiceResponse)
	if !ok {
		return 0, s.runtimeErr(ctx, fmt.Errorf("OServiceServiceBOS.ServiceRequest: unexpected response type %v", svcReqReplyBody))
	}

	loginCookie, hasCookie := svcReqReplyBody.Bytes(wire.OServiceTLVTagsLoginCookie)
	if !hasCookie {
		return 0, s.runtimeErr(ctx, errors.New("svcReqReplyBody.Bytes: missing wire.OServiceTLVTagsLoginCookie"))
	}

	chatSess, err := s.AuthService.RegisterChatSession(ctx, loginCookie)
	if err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("AuthService.RegisterChatSession: %w", err))
	}

	roomInfo := wire.ICBMRoomInfo{
		Exchange: inBody.Exchange,
		Cookie:   inBody.Cookie,
		Instance: inBody.InstanceNumber,
	}
	chatID := chatRegistry.Add(roomInfo)
	chatRegistry.RegisterSess(chatID, chatSess)

	if err := s.OServiceServiceChat.ClientOnline(ctx, wire.SNAC_0x01_0x02_OServiceClientOnline{}, chatSess); err != nil {
		return 0, s.runtimeErr(ctx, fmt.Errorf("OServiceServiceChat.ClientOnline: %w", err))
	}

	return chatID, fmt.Sprintf("CHAT_JOIN:%d:%s", chatID, roomName)
}

// ChatLeave handles the toc_chat_leave TOC command.
//
// From the TiK documentation:
//
//	Leave the chat room.
//
// Command syntax: toc_chat_leave <Chat Room ID>
func (s OSCARProxy) ChatLeave(ctx context.Context, chatRegistry *ChatRegistry, cmd []byte) string {
	var chatIDStr string

	if _, err := parseArgs(cmd, "toc_chat_leave", &chatIDStr); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	chatID, err := strconv.Atoi(chatIDStr)
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("strconv.Atoi: %w", err))
	}

	me := chatRegistry.RetrieveSess(chatID)
	if me == nil {
		return s.runtimeErr(ctx, fmt.Errorf("chatRegistry.RetrieveSess: chat session `%d` not found", chatID))
	}

	s.AuthService.SignoutChat(ctx, me)

	me.Close() // stop async server SNAC reply handler for this chat room

	return fmt.Sprintf("CHAT_LEFT:%d", chatID)
}

// ChatSend handles the toc_chat_send TOC command.
//
// From the TiK documentation:
//
//	Send a message in a chat room using the chat room id from CHAT_JOIN. Since
//	reflection is always on in TOC, you do not need to add the message to your
//	chat UI, since you will get a CHAT_IN with the message. Remember to quote
//	and encode the message.
//
// Command syntax: toc_chat_send <Chat Room ID> <Message>
func (s OSCARProxy) ChatSend(ctx context.Context, chatRegistry *ChatRegistry, cmd []byte) string {
	var chatIDStr, msg string

	if _, err := parseArgs(cmd, "toc_chat_send", &chatIDStr, &msg); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	chatID, err := strconv.Atoi(chatIDStr)
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("strconv.Atoi: %w", err))
	}

	me := chatRegistry.RetrieveSess(chatID)
	if me == nil {
		return s.runtimeErr(ctx, fmt.Errorf("chatRegistry.RetrieveSess: session for chat ID `%d` not found", chatID))
	}

	block := wire.TLVRestBlock{}
	// the order of these TLVs matters for AIM 2.x. if out of order, screen
	// names do not appear with each chat message.
	block.Append(wire.NewTLVBE(wire.ChatTLVEnableReflectionFlag, uint8(1)))
	block.Append(wire.NewTLVBE(wire.ChatTLVSenderInformation, me.TLVUserInfo()))
	block.Append(wire.NewTLVBE(wire.ChatTLVPublicWhisperFlag, []byte{}))
	block.Append(wire.NewTLVBE(wire.ChatTLVMessageInfo, wire.TLVRestBlock{
		TLVList: wire.TLVList{
			wire.NewTLVBE(wire.ChatTLVMessageInfoText, msg),
		},
	}))

	snac := wire.SNAC_0x0E_0x05_ChatChannelMsgToHost{
		Channel:      wire.ICBMChannelMIME,
		TLVRestBlock: block,
	}
	reply, err := s.ChatService.ChannelMsgToHost(ctx, me, wire.SNACFrame{}, snac)
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("ChatService.ChannelMsgToHost: %w", err))
	}

	if reply == nil {
		return s.runtimeErr(ctx, errors.New("ChatService.ChannelMsgToHost: missing response "))
	}

	switch v := reply.Body.(type) {
	case wire.SNAC_0x0E_0x06_ChatChannelMsgToClient:
		msgInfo, ok := v.Bytes(wire.ChatTLVMessageInfo)
		if !ok {
			return s.runtimeErr(ctx, errors.New("ChatService.ChannelMsgToHost: missing wire.ChatTLVMessageInfo"))
		}
		reflectMsg, err := wire.UnmarshalChatMessageText(msgInfo)
		if err != nil {
			return s.runtimeErr(ctx, fmt.Errorf("wire.UnmarshalChatMessageText: %w", err))
		}

		senderInfo, ok := v.Bytes(wire.ChatTLVSenderInformation)
		if !ok {
			return s.runtimeErr(ctx, errors.New("ChatService.ChannelMsgToHost: missing wire.ChatTLVSenderInformation"))
		}

		var userInfo wire.TLVUserInfo
		if err := wire.UnmarshalBE(&userInfo, bytes.NewReader(senderInfo)); err != nil {
			return s.runtimeErr(ctx, fmt.Errorf("wire.UnmarshalBE: %w", err))
		}

		return fmt.Sprintf("CHAT_IN:%d:%s:F:%s", chatID, userInfo.ScreenName, reflectMsg)
	default:
		return s.runtimeErr(ctx, errors.New("ChatService.ChannelMsgToHost: unexpected response"))
	}
}

// Evil handles the toc_evil TOC command.
//
// From the TiK documentation:
//
//	Evil/Warn someone else. The 2nd argument is either the string "norm" for a
//	normal warning, or "anon" for an anonymous warning. You can only evil
//	people who have recently sent you ims. The higher someones evil level, the
//	slower they can send message.
//
// Command syntax: toc_evil <User> <norm|anon>
func (s OSCARProxy) Evil(ctx context.Context, me *state.Session, cmd []byte) string {
	var user, scope string

	if _, err := parseArgs(cmd, "toc_evil", &user, &scope); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	snac := wire.SNAC_0x04_0x08_ICBMEvilRequest{
		ScreenName: user,
	}

	switch scope {
	case "anon":
		snac.SendAs = 1
	case "norm":
		snac.SendAs = 0
	default:
		return s.runtimeErr(ctx, fmt.Errorf("incorrect warning type `%s`. allowed values: anon, norm", scope))
	}

	response, err := s.ICBMService.EvilRequest(ctx, me, wire.SNACFrame{}, snac)
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("ICBMService.EvilRequest: %w", err))
	}

	switch v := response.Body.(type) {
	case wire.SNAC_0x04_0x09_ICBMEvilReply:
		return ""
	case wire.SNACError:
		s.Logger.InfoContext(ctx, "unable to warn user", "code", v.Code)
	default:
		return s.runtimeErr(ctx, errors.New("unexpected response"))
	}

	return ""
}

// GetDirSearchURL handles the toc_dir_search TOC command.
//
// From the TiK documentation:
//
//	Perform a search of the Oscar Directory, using colon separated fields as in:
//
//		"first name":"middle name":"last name":"maiden name":"city":"state":"country":"email"
//
// You can search by keyword by setting search terms in the 11th position (this
// feature is not in the TiK docs but is present in the code):
//
//	::::::::::"search kw"
//
//	Returns either a GOTO_URL or ERROR msg.
//
// Command syntax: toc_dir_search <info information>
func (s OSCARProxy) GetDirSearchURL(ctx context.Context, me *state.Session, cmd []byte) string {
	var info string

	if _, err := parseArgs(cmd, "toc_dir_search", &info); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	params := strings.Split(info, ":")
	labels := []string{
		"first_name",
		"middle_name",
		"last_name",
		"maiden_name",
		"city",
		"state",
		"country",
		"email",
		"nop", // unused placeholder
		"nop",
		"keyword",
	}

	// map labels to param values at their corresponding positions
	p := url.Values{}
	for i, param := range params {
		if i >= len(labels) {
			break
		}
		if param != "" {
			p.Add(labels[i], strings.Trim(param, "\""))
		}
	}

	if len(p) == 0 {
		return s.runtimeErr(ctx, errors.New("no search fields found"))
	}

	cookie, err := s.newHTTPAuthToken(me.IdentScreenName())
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("newHTTPAuthToken: %w", err))
	}
	p.Add("cookie", cookie)

	return fmt.Sprintf("GOTO_URL:search results:dir_search?%s", p.Encode())
}

// GetDirURL handles the toc_get_dir TOC command.
//
// From the TiK documentation:
//
//	Gets a user's dir info a GOTO_URL or ERROR message will be sent back to the client.
//
// Command syntax: toc_get_dir <username>
func (s OSCARProxy) GetDirURL(ctx context.Context, me *state.Session, cmd []byte) string {
	var user string

	if _, err := parseArgs(cmd, "toc_get_dir", &user); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	cookie, err := s.newHTTPAuthToken(me.IdentScreenName())
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("newHTTPAuthToken: %w", err))
	}

	p := url.Values{}
	p.Add("cookie", cookie)
	p.Add("user", user)

	return fmt.Sprintf("GOTO_URL:directory info:dir_info?%s", p.Encode())
}

// GetInfoURL handles the toc_get_info TOC command.
//
// From the TiK documentation:
//
//	Gets a user's info a GOTO_URL or ERROR message will be sent back to the client.
//
// Command syntax: toc_get_info <username>
func (s OSCARProxy) GetInfoURL(ctx context.Context, me *state.Session, cmd []byte) string {
	var user string

	if _, err := parseArgs(cmd, "toc_get_info", &user); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	cookie, err := s.newHTTPAuthToken(me.IdentScreenName())
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("newHTTPAuthToken: %w", err))
	}

	p := url.Values{}
	p.Add("cookie", cookie)
	p.Add("from", me.IdentScreenName().String())
	p.Add("user", user)

	return fmt.Sprintf("GOTO_URL:profile:info?%s", p.Encode())
}

// InitDone handles the toc_init_done TOC command.
//
// From the TiK documentation:
//
//	Tells TOC that we are ready to go online. TOC clients should first send TOC
//	the buddy list and any permit/deny lists. However, toc_init_done must be
//	called within 30 seconds after toc_signon, or the connection will be
//	dropped. Remember, it can't be called until after the SIGN_ON message is
//	received. Calling this before or multiple times after a SIGN_ON will cause
//	the connection to be dropped.
//
// Note: The business logic described in the last 3 sentences are not yet
// implemented.
//
// Command syntax: toc_init_done
func (s OSCARProxy) InitDone(ctx context.Context, sess *state.Session, cmd []byte) string {
	if _, err := parseArgs(cmd, "toc_init_done"); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}
	if err := s.OServiceServiceBOS.ClientOnline(ctx, wire.SNAC_0x01_0x02_OServiceClientOnline{}, sess); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("OServiceServiceBOS.ClientOnliney: %w", err))
	}
	return ""
}

// RemoveBuddy handles the toc_remove_buddy TOC command.
//
// From the TiK documentation:
//
//	Remove buddies from your buddy list. This does not change your saved config.
//
// Command syntax:
func (s OSCARProxy) RemoveBuddy(ctx context.Context, me *state.Session, cmd []byte) string {
	users, err := parseArgs(cmd, "toc_remove_buddy")
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	snac := wire.SNAC_0x03_0x05_BuddyDelBuddies{}
	for _, sn := range users {
		snac.Buddies = append(snac.Buddies, struct {
			ScreenName string `oscar:"len_prefix=uint8"`
		}{ScreenName: sn})
	}

	if err := s.BuddyService.DelBuddies(ctx, me, snac); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("BuddyService.DelBuddies: %w", err))
	}
	return ""
}

// SendIM handles the toc_send_im TOC command.
//
// From the TiK documentation:
//
//	Send a message to a remote user. Remember to quote and encode the message.
//	If the optional string "auto" is the last argument, then the auto response
//	flag will be turned on for the IM.
//
// Command syntax: toc_send_im <Destination User> <Message> [auto]
func (s OSCARProxy) SendIM(ctx context.Context, sender *state.Session, cmd []byte) string {
	var recip, msg string

	autoReply, err := parseArgs(cmd, "toc_send_im", &recip, &msg)
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	frags, err := wire.ICBMFragmentList(msg)
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("wire.ICBMFragmentList: %w", err))
	}

	snac := wire.SNAC_0x04_0x06_ICBMChannelMsgToHost{
		ChannelID:  wire.ICBMChannelIM,
		ScreenName: recip,
		TLVRestBlock: wire.TLVRestBlock{
			TLVList: wire.TLVList{
				wire.NewTLVBE(wire.ICBMTLVAOLIMData, frags),
			},
		},
	}

	if len(autoReply) > 0 && autoReply[0] == "auto" {
		snac.Append(wire.NewTLVBE(wire.ICBMTLVAutoResponse, []byte{}))
	}

	// send message and ignore response since there is no TOC error code to
	// handle errors such as "user is offline", etc.
	_, err = s.ICBMService.ChannelMsgToHost(ctx, sender, wire.SNACFrame{}, snac)
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("ICBMService.ChannelMsgToHost: %w", err))
	}

	return ""
}

// SetAway handles the toc_chat_join TOC command.
//
// From the TiK documentation:
//
//	If the away message is present, then the unavailable status flag is set for
//	the user. If the away message is not present, then the unavailable status
//	flag is unset. The away message is basic HTML, remember to encode the
//	information.
//
// Command syntax: toc_set_away [<away message>]
func (s OSCARProxy) SetAway(ctx context.Context, me *state.Session, cmd []byte) string {
	maybeMsg, err := parseArgs(cmd, "toc_set_away")
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	var msg string
	if len(maybeMsg) > 0 {
		msg = maybeMsg[0]
	}

	snac := wire.SNAC_0x02_0x04_LocateSetInfo{
		TLVRestBlock: wire.TLVRestBlock{
			TLVList: wire.TLVList{
				wire.NewTLVBE(wire.LocateTLVTagsInfoUnavailableData, msg),
			},
		},
	}

	if err := s.LocateService.SetInfo(ctx, me, snac); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("LocateService.SetInfo: %w", err))
	}

	return ""
}

// SetCaps handles the toc_set_caps TOC command.
//
// From the TiK documentation:
//
//	Set my capabilities. All capabilities that we support need to be sent at
//	the same time. Capabilities are represented by UUIDs.
//
// This method automatically adds the "chat" capability since it doesn't seem
// to be sent explicitly by the official clients, even though they support
// chat.
//
// Command syntax: toc_set_caps [ <Capability 1> [<Capability 2> [...]]]
func (s OSCARProxy) SetCaps(ctx context.Context, me *state.Session, cmd []byte) string {
	params, err := parseArgs(cmd, "toc_set_caps")
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	caps := make([]uuid.UUID, 0, 16*(len(params)+1))
	for _, capStr := range params {
		uid, err := uuid.Parse(capStr)
		if err != nil {
			return s.runtimeErr(ctx, fmt.Errorf("UUID.Parse: %w", err))
		}
		caps = append(caps, uid)
	}
	caps = append(caps, capChat)

	snac := wire.SNAC_0x02_0x04_LocateSetInfo{
		TLVRestBlock: wire.TLVRestBlock{
			TLVList: wire.TLVList{
				wire.NewTLVBE(wire.LocateTLVTagsInfoCapabilities, caps),
			},
		},
	}

	if err := s.LocateService.SetInfo(ctx, me, snac); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("LocateService.SetInfo: %w", err))
	}

	return ""
}

// SetConfig handles the toc_set_config TOC command.
//
// From the TiK documentation:
//
//	Set the config information for this user. The config information is line
//	oriented with the first character being the item type, followed by a space,
//	with the rest of the line being the item value. Only letters, numbers, and
//	spaces should be used. Remember you will have to enclose the entire config
//	in quotes.
//
//	Item Types:
//		- g - Buddy Group (All Buddies until the next g or the end of config are in this group.)
//		- b - A Buddy
//		- p - Person on permit list
//		- d - Person on deny list
//		- m - Permit/Deny Mode. Possible values are
//		- 1 - Permit All
//		- 2 - Deny All
//		- 3 - Permit Some
//		- 4 - Deny Some
//
// Command syntax: toc_set_config <Config Info>
func (s OSCARProxy) SetConfig(ctx context.Context, me *state.Session, cmd []byte) string {
	// replace curly braces with quotes so that the string can be properly
	// split up by the space-delimited reader
	for i, c := range cmd {
		if c == '{' || c == '}' {
			cmd[i] = '"'
		}
	}
	cmd = bytes.TrimSpace(cmd)

	var info string
	if _, err := parseArgs(cmd, "toc_set_config", &info); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	config := strings.Split(info, "\n")

	var cfg [][2]string
	for _, item := range config {
		parts := strings.Split(item, " ")
		if len(parts) != 2 {
			s.Logger.InfoContext(ctx, "invalid config item", "item", item, "user", me.DisplayScreenName())
			continue
		}
		cfg = append(cfg, [2]string{parts[0], parts[1]})
	}

	mode := wire.FeedbagPDModePermitAll
	for _, c := range cfg {
		if c[0] != "m" {
			continue
		}
		switch c[1] {
		case "1":
			mode = wire.FeedbagPDModePermitAll
		case "2":
			mode = wire.FeedbagPDModeDenyAll
		case "3":
			mode = wire.FeedbagPDModePermitSome
		case "4":
			mode = wire.FeedbagPDModeDenySome
		default:
			return s.runtimeErr(ctx, fmt.Errorf("config: invalid mode `%s`", c[1]))
		}
	}

	switch mode {
	case wire.FeedbagPDModePermitAll:
		snac := wire.SNAC_0x09_0x07_PermitDenyAddDenyListEntries{
			Users: []struct {
				ScreenName string `oscar:"len_prefix=uint8"`
			}{
				{
					ScreenName: me.IdentScreenName().String(),
				},
			},
		}
		if err := s.PermitDenyService.AddDenyListEntries(ctx, me, snac); err != nil {
			return s.runtimeErr(ctx, fmt.Errorf("PermitDenyService.AddDenyListEntries: %w", err))
		}
	case wire.FeedbagPDModeDenyAll:
		snac := wire.SNAC_0x09_0x05_PermitDenyAddPermListEntries{
			Users: []struct {
				ScreenName string `oscar:"len_prefix=uint8"`
			}{
				{
					ScreenName: me.IdentScreenName().String(),
				},
			},
		}
		if err := s.PermitDenyService.AddPermListEntries(ctx, me, snac); err != nil {
			return s.runtimeErr(ctx, fmt.Errorf("PermitDenyService.AddPermListEntrie: %w", err))
		}
	case wire.FeedbagPDModePermitSome:
		snac := wire.SNAC_0x09_0x05_PermitDenyAddPermListEntries{}
		for _, c := range cfg {
			if c[0] != "p" {
				continue
			}
			snac.Users = append(snac.Users, struct {
				ScreenName string `oscar:"len_prefix=uint8"`
			}{ScreenName: c[1]})
		}
		if err := s.PermitDenyService.AddPermListEntries(ctx, me, snac); err != nil {
			return s.runtimeErr(ctx, fmt.Errorf("PermitDenyService.AddPermListEntrie: %w", err))
		}
	case wire.FeedbagPDModeDenySome:
		snac := wire.SNAC_0x09_0x07_PermitDenyAddDenyListEntries{}
		for _, c := range cfg {
			if c[0] != "d" {
				continue
			}
			snac.Users = append(snac.Users, struct {
				ScreenName string `oscar:"len_prefix=uint8"`
			}{ScreenName: c[1]})
		}
		if err := s.PermitDenyService.AddDenyListEntries(ctx, me, snac); err != nil {
			return s.runtimeErr(ctx, fmt.Errorf("PermitDenyService.AddDenyListEntries: %w", err))
		}
	}

	snac := wire.SNAC_0x03_0x04_BuddyAddBuddies{}
	for _, c := range cfg {
		if c[0] != "b" {
			continue
		}
		snac.Buddies = append(snac.Buddies, struct {
			ScreenName string `oscar:"len_prefix=uint8"`
		}{ScreenName: c[1]})
	}

	if err := s.BuddyService.AddBuddies(ctx, me, snac); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("BuddyService.AddBuddies: %w", err))
	}

	if err := s.TOCConfigStore.SetTOCConfig(me.IdentScreenName(), info); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("TOCConfigStore.SaveTOCConfig: %w", err))
	}

	return ""
}

// SetDir handles the toc_set_dir TOC command.
//
// From the TiK documentation:
//
//	Set the DIR user information. This is a colon separated fields as in:
//
//		"first name":"middle name":"last name":"maiden name":"city":"state":"country":"email":"allow web searches".
//
//	Should return a DIR_STATUS msg. Having anything in the "allow web searches"
//	field allows people to use web-searches to find your directory info.
//	Otherwise, they'd have to use the client.
//
// The fields "email" and "allow web searches" are ignored by this method.
//
// Command syntax: toc_set_dir <info information>
func (s OSCARProxy) SetDir(ctx context.Context, me *state.Session, cmd []byte) string {
	var info string

	if _, err := parseArgs(cmd, "toc_set_dir", &info); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	rawFields := strings.Split(info, ":")

	var finalFields [9]string

	if len(rawFields) > len(finalFields) {
		return s.runtimeErr(ctx, fmt.Errorf("expected at most %d params, got %d", len(finalFields), len(rawFields)))
	}
	for i, a := range rawFields {
		finalFields[i] = strings.Trim(a, "\"")
	}

	snac := wire.SNAC_0x02_0x09_LocateSetDirInfo{
		TLVRestBlock: wire.TLVRestBlock{
			TLVList: wire.TLVList{
				wire.NewTLVBE(wire.ODirTLVFirstName, finalFields[0]),
				wire.NewTLVBE(wire.ODirTLVMiddleName, finalFields[1]),
				wire.NewTLVBE(wire.ODirTLVLastName, finalFields[2]),
				wire.NewTLVBE(wire.ODirTLVMaidenName, finalFields[3]),
				wire.NewTLVBE(wire.ODirTLVCountry, finalFields[6]),
				wire.NewTLVBE(wire.ODirTLVState, finalFields[5]),
				wire.NewTLVBE(wire.ODirTLVCity, finalFields[4]),
			},
		},
	}
	if _, err := s.LocateService.SetDirInfo(ctx, me, wire.SNACFrame{}, snac); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("LocateService.SetDirInfo: %w", err))
	}

	return ""
}

// SetIdle handles the toc_set_idle TOC command.
//
// From the TiK documentation:
//
//	Set idle information. If <idle secs> is 0 then the user isn't idle at all.
//	If <idle secs> is greater than 0 then the user has already been idle for
//	<idle secs> number of seconds. The server will automatically keep
//	incrementing this number, so do not repeatedly call with new idle times.
//
// Command syntax: toc_set_idle <idle secs>
func (s OSCARProxy) SetIdle(ctx context.Context, me *state.Session, cmd []byte) string {
	var idleTimeStr string

	if _, err := parseArgs(cmd, "toc_set_idle", &idleTimeStr); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	time, err := strconv.Atoi(idleTimeStr)
	if err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("strconv.Atoi: %w", err))
	}

	snac := wire.SNAC_0x01_0x11_OServiceIdleNotification{
		IdleTime: uint32(time),
	}
	if err := s.OServiceServiceBOS.IdleNotification(ctx, me, snac); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("OServiceServiceBOS.IdleNotification: %w", err))
	}

	return ""
}

// SetInfo handles the toc_set_info TOC command.
//
// From the TiK documentation:
//
//	Set the LOCATE user information. This is basic HTML. Remember to encode the info.
//
// Command syntax: toc_set_info <info information>
func (s OSCARProxy) SetInfo(ctx context.Context, me *state.Session, cmd []byte) string {
	var info string

	if _, err := parseArgs(cmd, "toc_set_info", &info); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))
	}

	snac := wire.SNAC_0x02_0x04_LocateSetInfo{
		TLVRestBlock: wire.TLVRestBlock{
			TLVList: wire.TLVList{
				wire.NewTLVBE(wire.LocateTLVTagsInfoSigData, info),
			},
		},
	}
	if err := s.LocateService.SetInfo(ctx, me, snac); err != nil {
		return s.runtimeErr(ctx, fmt.Errorf("LocateService.SetInfo: %w", err))
	}

	return ""
}

// Signon handles the toc_signon TOC command.
//
// From the TiK documentation:
//
//	The password needs to be roasted with the Roasting String if coming over a
//	FLAP connection, CP connections don't use roasted passwords. The language
//	specified will be used when generating web pages, such as the get info
//	pages. Currently, the only supported language is "english". If the language
//	sent isn't found, the default "english" language will be used. The version
//	string will be used for the client identity, and must be less than 50
//	characters.
//
//	Passwords are roasted when sent to the host. This is done so they aren't
//	sent in "clear text" over the wire, although they are still trivial to
//	decode. Roasting is performed by first xoring each byte in the password
//	with the equivalent modulo byte in the roasting string. The result is then
//	converted to ascii hex, and prepended with "0x". So for example the
//	password "password" roasts to "0x2408105c23001130".
//
//	The Roasting String is Tic/Toc.
//
// Command syntax: toc_signon <authorizer host> <authorizer port> <User Name> <Password> <language> <version>
func (s OSCARProxy) Signon(ctx context.Context, cmd []byte) (*state.Session, []string) {
	var userName, password string

	if _, err := parseArgs(cmd, "toc_signon", nil, nil, &userName, &password); err != nil {
		return nil, []string{s.runtimeErr(ctx, fmt.Errorf("parseArgs: %w", err))}
	}

	passwordHash, err := hex.DecodeString(password[2:])
	if err != nil {
		return nil, []string{s.runtimeErr(ctx, fmt.Errorf("hex.DecodeString: %w", err))}
	}

	signonFrame := wire.FLAPSignonFrame{}
	signonFrame.Append(wire.NewTLVBE(wire.LoginTLVTagsScreenName, userName))
	signonFrame.Append(wire.NewTLVBE(wire.LoginTLVTagsRoastedTOCPassword, passwordHash))

	block, err := s.AuthService.FLAPLogin(signonFrame, state.NewStubUser)
	if err != nil {
		return nil, []string{s.runtimeErr(ctx, fmt.Errorf("AuthService.FLAPLogin: %w", err))}
	}

	if block.HasTag(wire.LoginTLVTagsErrorSubcode) {
		s.Logger.DebugContext(ctx, "login failed")
		return nil, []string{"ERROR:980"} // bad username/password
	}

	authCookie, ok := block.Bytes(wire.OServiceTLVTagsLoginCookie)
	if !ok {
		return nil, []string{s.runtimeErr(ctx, fmt.Errorf("unable to get session id from payload"))}
	}

	sess, err := s.AuthService.RegisterBOSSession(authCookie)
	if err != nil {
		return nil, []string{s.runtimeErr(ctx, fmt.Errorf("AuthService.RegisterBOSSession: %w", err))}
	}

	// set chat capability so that... tk
	sess.SetCaps([][16]byte{capChat})

	if err := s.BuddyListRegistry.RegisterBuddyList(sess.IdentScreenName()); err != nil {
		return nil, []string{s.runtimeErr(ctx, fmt.Errorf("BuddyListRegistry.RegisterBuddyList: %w", err))}
	}

	u, err := s.TOCConfigStore.User(sess.IdentScreenName())
	if err != nil {
		return nil, []string{s.runtimeErr(ctx, fmt.Errorf("TOCConfigStore.User: %w", err))}
	}
	if u == nil {
		return nil, []string{s.runtimeErr(ctx, fmt.Errorf("TOCConfigStore.User: user not found"))}
	}

	return sess, []string{"SIGN_ON:TOC1.0", fmt.Sprintf("CONFIG:%s", u.TOCConfig)}
}

// Signout terminates a TOC session. It sends departure notifications to
// buddies, de-registers buddy list and session.
func (s OSCARProxy) Signout(ctx context.Context, me *state.Session) {
	if err := s.BuddyListRegistry.UnregisterBuddyList(me.IdentScreenName()); err != nil {
		s.Logger.ErrorContext(ctx, "error removing buddy list entry", "err", err.Error())
	}
	s.AuthService.Signout(ctx, me)
}

// newHTTPAuthToken creates a HMAC token for authenticating TOC HTTP requests
func (s OSCARProxy) newHTTPAuthToken(me state.IdentScreenName) (string, error) {
	cookie, err := s.CookieBaker.Issue([]byte(me.String()))
	if err != nil {
		return "", err
	}
	// trim padding so that gaim doesn't choke on the long value
	cookie = bytes.TrimRight(cookie, "\x00")
	return hex.EncodeToString(cookie), nil
}

// parseArgs extracts arguments from a TOC command. Each positional argument is
// assigned to its corresponding args pointer. It returns the remaining
// arguments as varargs.
func parseArgs(payload []byte, cmd string, args ...*string) (varArgs []string, err error) {
	reader := csv.NewReader(bytes.NewReader(payload))
	reader.Comma = ' '
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	segs, err := reader.Read()
	if err != nil {
		return []string{}, fmt.Errorf("CSV reader error: %w", err)
	}

	// sanity check the command name
	if segs[0] != cmd {
		return []string{}, fmt.Errorf("command mismatch. expected %s, got %s", cmd, segs[0])
	}

	// all elements after the command are arguments
	segs = segs[1:]
	if len(segs) < len(args) {
		return []string{}, fmt.Errorf("command contains fewer arguments than expected")
	}

	// populate placeholder pointers with their corresponding values
	for i, arg := range args {
		if arg != nil {
			*arg = strings.TrimSpace(segs[i])
		}
	}

	// dump remaining arguments as varargs
	return segs[len(args):], err
}

// runtimeErr is a convenience function that logs an error and returns a TOC
// internal server error.
func (s OSCARProxy) runtimeErr(ctx context.Context, err error) string {
	s.Logger.ErrorContext(ctx, "internal service error", "err", err.Error())
	return cmdInternalSvcErr
}
