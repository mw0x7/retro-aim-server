package oscar

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"reflect"
	"time"
)

const (
	ChatNavErr                 uint16 = 0x0001
	ChatNavRequestChatRights          = 0x0002
	ChatNavRequestExchangeInfo        = 0x0003
	ChatNavRequestRoomInfo            = 0x0004
	ChatNavRequestMoreRoomInfo        = 0x0005
	ChatNavRequestOccupantList        = 0x0006
	ChatNavSearchForRoom              = 0x0007
	ChatNavCreateRoom                 = 0x0008
	ChatNavNavInfo                    = 0x0009
)

func routeChatNav(flap *flapFrame, snac *snacFrame, r io.Reader, w io.Writer, sequence *uint32) error {
	switch snac.subGroup {
	case ChatNavErr:
		panic("not implemented")
	case ChatNavRequestChatRights:
		return SendAndReceiveNextChatRights(flap, snac, r, w, sequence)
	case ChatNavRequestExchangeInfo:
		panic("not implemented")
	case ChatNavRequestRoomInfo:
		panic("not implemented")
	case ChatNavRequestMoreRoomInfo:
		panic("not implemented")
	case ChatNavRequestOccupantList:
		panic("not implemented")
	case ChatNavSearchForRoom:
		panic("not implemented")
	case ChatNavCreateRoom:
		return SendAndReceiveCreateRoom(flap, snac, r, w, sequence)
	case ChatNavNavInfo:
		panic("not implemented")
	}
	return nil
}

type exchangeInfo struct {
	identifier uint16
	TLVPayload
}

func (s *exchangeInfo) write(w io.Writer) error {
	if err := binary.Write(w, binary.BigEndian, s.identifier); err != nil {
		return err
	}
	//if err := binary.Write(w, binary.BigEndian, uint8(len(s.TLVs))); err != nil {
	//	return err
	//}
	return s.TLVPayload.write(w)
}

type roomInfo struct {
	exchange       uint16
	cookie         []byte
	instanceNumber uint16
}

func (s *roomInfo) write(w io.Writer) error {
	if err := binary.Write(w, binary.BigEndian, s.exchange); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint8(len(s.cookie))); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, s.cookie); err != nil {
		return err
	}
	return binary.Write(w, binary.BigEndian, s.instanceNumber)
}

func SendAndReceiveNextChatRights(flap *flapFrame, snac *snacFrame, r io.Reader, w io.Writer, sequence *uint32) error {
	fmt.Printf("sendAndReceiveNextChatRights read SNAC frame: %+v\n", snac)

	snacFrameOut := snacFrame{
		foodGroup: CHAT_NAV,
		subGroup:  ChatNavNavInfo,
	}

	//rInfo := roomInfo{
	//	exchange:       4,
	//	cookie:         []byte("create"),
	//	instanceNumber: 65535,
	//}
	//buf1 := &bytes.Buffer{}
	//if err := rInfo.write(buf1); err != nil {
	//	return err
	//}
	//
	//xInfo := exchangeInfo{
	//	identifier: 4,
	//	TLVPayload: TLVPayload{
	//		TLVs: []*TLV{
	//			{
	//				tType: 0x05,
	//				val:   buf1.Bytes(),
	//			},
	//			{
	//				tType: 0x00d1,
	//				val:   uint16(100),
	//			},
	//			{
	//				tType: 0x000a,
	//				val:   uint16(0x0114),
	//			},
	//			{
	//				tType: 0x00d2,
	//				val:   uint16(10),
	//			},
	//			{
	//				tType: 0x02,
	//				val:   uint16(1),
	//			},
	//			{
	//				tType: 0xc9,
	//				val:   uint16(63),
	//			},
	//			{
	//				tType: 0xd3,
	//				val:   "create",
	//			},
	//			{
	//				tType: 0xd5,
	//				val:   uint8(1),
	//			},
	//			{
	//				tType: 0xd6,
	//				val:   "us-ascii",
	//			},
	//			{
	//				tType: 0xd7,
	//				val:   "en",
	//			},
	//			{
	//				tType: 0xd8,
	//				val:   "us-ascii",
	//			},
	//			{
	//				tType: 0xd9,
	//				val:   "en",
	//			},
	//		},
	//	},
	//}
	//
	//buf := &bytes.Buffer{}
	//if err := xInfo.write(buf); err != nil {
	//	return err
	//}
	//
	//roomBuf := &bytes.Buffer{}
	//newRoomInfo := &snacCreateRoom{
	//	exchange:       4,
	//	cookie:         []byte("create"),
	//	instanceNumber: 65535,
	//	detailLevel:    2,
	//	TLVPayload: TLVPayload{
	//		TLVs: []*TLV{
	//			{
	//				tType: uint16(0xD3),
	//				val:   "hahah new name",
	//			},
	//		},
	//	},
	//}
	//if err := newRoomInfo.write(roomBuf); err != nil {
	//	return err
	//}

	xchange := TLVPayload{
		TLVs: []*TLV{
			{
				tType: 0x000a,
				val:   uint16(0x0114),
			},
			//{
			//	tType: 0x000d,
			//	val:   nil,
			//},
			{
				tType: 0x0004,
				val:   uint8(15),
			},
			{
				tType: 0x0002,
				val:   uint16(0x0010),
			},
			{
				tType: 0x00c9,
				val:   uint16(15),
			},
			{
				tType: 0x00ca,
				val:   uint32(time.Now().Unix()),
			},
			//{
			//	tType: 0x00d0,
			//	val:   nil,
			//},
			{
				tType: 0x00d1,
				val:   uint16(1024),
			},
			{
				tType: 0x00d2,
				val:   uint16(100),
			},
			{
				tType: 0x00d3,
				val:   "hello",
			},
			{
				tType: 0x00d4,
				val:   "http://www.google.com",
			},
			{
				tType: 0x00d5,
				val:   uint8(2),
			},
			{
				tType: 0xd6,
				val:   "us-ascii",
			},
			{
				tType: 0xd7,
				val:   "en",
			},
			{
				tType: 0xd8,
				val:   "us-ascii",
			},
			{
				tType: 0xd9,
				val:   "en",
			},
			{
				tType: 0x00da,
				val:   uint16(0),
			},
		},
	}

	roomBuf := &bytes.Buffer{}
	if err := binary.Write(roomBuf, binary.BigEndian, uint16(4)); err != nil {
		return err
	}
	if err := xchange.write(roomBuf); err != nil {
		return err
	}

	snacPayloadOut := &TLVPayload{
		TLVs: []*TLV{
			{
				tType: 0x02,
				val:   uint8(10),
			},
			{
				tType: 0x03,
				val:   roomBuf.Bytes(),
			},
		},
	}

	return writeOutSNAC(snac, flap, snacFrameOut, snacPayloadOut, sequence, w)
}

type snacCreateRoom struct {
	exchange       uint16
	cookie         []byte
	instanceNumber uint16
	detailLevel    uint8
	TLVPayload
}

func (s *snacCreateRoom) read(r io.Reader) error {
	if err := binary.Read(r, binary.BigEndian, &s.exchange); err != nil {
		return err
	}
	var l uint8
	if err := binary.Read(r, binary.BigEndian, &l); err != nil {
		return err
	}
	s.cookie = make([]byte, l)
	if _, err := r.Read(s.cookie); err != nil {
		return err
	}
	if err := binary.Read(r, binary.BigEndian, &s.instanceNumber); err != nil {
		return err
	}
	if err := binary.Read(r, binary.BigEndian, &s.detailLevel); err != nil {
		return err
	}

	var tlvCount uint16
	if err := binary.Read(r, binary.BigEndian, &tlvCount); err != nil {
		return err
	}

	return s.TLVPayload.read(r, map[uint16]reflect.Kind{
		0x00d3: reflect.String, // name
		0x00d6: reflect.String, // charset
		0x00d7: reflect.String, // lang
	})
}

func (s *snacCreateRoom) write(w io.Writer) error {
	if err := binary.Write(w, binary.BigEndian, s.exchange); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint8(len(s.cookie))); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, s.cookie); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, s.instanceNumber); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, s.detailLevel); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint16(len(s.TLVs))); err != nil {
		return err
	}
	return s.TLVPayload.write(w)
}

func SendAndReceiveCreateRoom(flap *flapFrame, snac *snacFrame, r io.Reader, w io.Writer, sequence *uint32) error {
	fmt.Printf("SendAndReceiveCreateRoom read SNAC frame: %+v\n", snac)

	snacPayloadIn := &snacCreateRoom{}
	if err := snacPayloadIn.read(r); err != nil {
		return err
	}

	name, _ := snacPayloadIn.getString(0x00d3)
	//charset, _ := snacPayloadIn.getString(0x00d6)
	//lang, _ := snacPayloadIn.getString(0x00d7)

	snacPayloadIn.TLVPayload = TLVPayload{
		[]*TLV{
			//{
			//	tType: 0x00d3,
			//	val:   name,
			//},
			//{
			//	tType: 0x00d6,
			//	val:   charset,
			//},
			//{
			//	tType: 0x00d7,
			//	val:   lang,
			//},
			{
				tType: 0x006a,
				val:   name,
			},
			{
				tType: 0x00c9,
				val:   uint16(0),
			},
			{
				tType: 0x00ca,
				val:   uint32(time.Now().Unix()),
			},
			{
				tType: 0x00d1,
				val:   uint16(100),
			},
			{
				tType: 0x00d2,
				val:   uint16(100),
			},
			{
				tType: 0x00d3,
				val:   name,
			},
			{
				tType: 0x00d5,
				val:   uint8(2),
			},
		},
	}

	snacPayloadIn.detailLevel = 0x02

	buf := &bytes.Buffer{}
	if err := snacPayloadIn.write(buf); err != nil {
		return err
	}

	snacOut := &TLVPayload{
		[]*TLV{
			{
				tType: 0x0004,
				val:   buf.Bytes(),
			},
		},
	}

	snacFrameOut := snacFrame{
		foodGroup: CHAT_NAV,
		subGroup:  ChatNavCreateRoom,
	}

	return writeOutSNAC(snac, flap, snacFrameOut, snacOut, sequence, w)
}
