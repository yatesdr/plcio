package eipadapter

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"

	"github.com/yatesdr/plcio/eip"
	"github.com/yatesdr/plcio/logging"
)

// serveTCP accepts incoming connections and spawns a goroutine per session.
func (a *Adapter) serveTCP(ctx context.Context) {
	defer a.wg.Done()
	for {
		_ = a.tcpListener.SetDeadline(time.Now().Add(500 * time.Millisecond))
		conn, err := a.tcpListener.Accept()
		if err != nil {
			if isClosedNetErr(err) || ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			logging.DebugError("eipadapter", "accept", err)
			continue
		}
		logging.DebugLog("eipadapter", "TCP accept %s", conn.RemoteAddr())
		a.wg.Add(1)
		go a.handleConn(ctx, conn)
	}
}

// handleConn services a single TCP session until close or error.
func (a *Adapter) handleConn(ctx context.Context, conn net.Conn) {
	defer a.wg.Done()
	defer conn.Close()

	var localSession uint32

	for {
		if ctx.Err() != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		frame, err := eip.ReadFrame(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !isClosedNetErr(err) {
				logging.DebugLog("eipadapter", "session %08X read: %v", localSession, err)
			}
			break
		}

		if localSession == 0 && frame.Command != eip.RegisterSession && frame.Command != uint16(0x63) /* ListIdentity */ && frame.Command != uint16(0x00) /* NOP */ && frame.Command != uint16(0x04) /* ListServices */ && frame.Command != uint16(0x64) /* ListInterfaces */ {
			a.sendFrame(conn, frame.Reply(eip.EncapStatusInvalidSession, nil))
			continue
		}

		if frame.SessionHandle != 0 && localSession != 0 && frame.SessionHandle != localSession {
			a.sendFrame(conn, frame.Reply(eip.EncapStatusInvalidSession, nil))
			continue
		}

		switch frame.Command {
		case uint16(0x00): // NOP — no response per spec
			continue
		case eip.RegisterSession:
			localSession = a.handleRegisterSession(conn, frame)
		case eip.UnRegisterSession:
			a.handleUnregisterSession(localSession, frame)
			return
		case uint16(0x04): // ListServices
			a.sendFrame(conn, buildListServicesReply(frame))
		case uint16(0x63): // ListIdentity over TCP
			a.sendFrame(conn, buildListIdentityReply(frame, &a.cfg.Identity))
		case uint16(0x64): // ListInterfaces — empty CPF
			data := make([]byte, 0, 2)
			data = binary.LittleEndian.AppendUint16(data, 0) // item count = 0
			a.sendFrame(conn, frame.Reply(eip.EncapStatusSuccess, data))
		case eip.SendRRData:
			a.handleSendRRData(conn, frame)
		case eip.SendUnitData:
			a.handleSendUnitData(conn, frame)
		default:
			a.sendFrame(conn, frame.Reply(eip.EncapStatusInvalidCommand, nil))
		}
	}

	if localSession != 0 {
		a.sessions.remove(localSession)
	}
}

func (a *Adapter) handleRegisterSession(conn net.Conn, f *eip.Frame) uint32 {
	// Payload: protocol_version(uint16) + options(uint16). We support v1 only.
	if len(f.Data) < 4 {
		a.sendFrame(conn, f.Reply(eip.EncapStatusInvalidLength, nil))
		return 0
	}
	ver := binary.LittleEndian.Uint16(f.Data[0:2])
	if ver != 1 {
		a.sendFrame(conn, f.Reply(eip.EncapStatusUnsupportedRev, nil))
		return 0
	}
	h := a.sessions.register(conn)
	logging.DebugLog("eipadapter", "RegisterSession granted 0x%08X to %s", h, conn.RemoteAddr())
	reply := &eip.Frame{
		Command:       eip.RegisterSession,
		SessionHandle: h,
		Status:        eip.EncapStatusSuccess,
		Context:       f.Context,
		Options:       f.Options,
		Data:          []byte{0x01, 0x00, 0x00, 0x00},
	}
	a.sendFrame(conn, reply)
	return h
}

func (a *Adapter) handleUnregisterSession(localSession uint32, f *eip.Frame) {
	if localSession != 0 {
		a.sessions.remove(localSession)
		logging.DebugLog("eipadapter", "UnRegisterSession 0x%08X", localSession)
	}
	// Per spec, no response — peer expects connection close.
}

// sendFrame writes a frame back to the peer and ignores write errors (the
// session is torn down on the next iteration anyway).
func (a *Adapter) sendFrame(conn net.Conn, f *eip.Frame) {
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write(f.Bytes())
}

// buildListServicesReply builds a minimal ListServices response advertising
// support for Communications (EtherNet/IP) and CIP transport.
func buildListServicesReply(req *eip.Frame) *eip.Frame {
	// One CPF item, type 0x0100 (List Services Response):
	//   protocol_version (UINT) = 1
	//   capability_flags (UINT) = 0x0020 (supports CIP encapsulation over TCP)
	//   name_of_service[16] = "Communications" + NULs
	body := make([]byte, 0, 24)
	body = binary.LittleEndian.AppendUint16(body, 1)
	body = binary.LittleEndian.AppendUint16(body, 0x0020)
	name := make([]byte, 16)
	copy(name, []byte("Communications"))
	body = append(body, name...)

	cpf := make([]byte, 0, 6+len(body))
	cpf = binary.LittleEndian.AppendUint16(cpf, 1) // item count
	cpf = binary.LittleEndian.AppendUint16(cpf, 0x0100)
	cpf = binary.LittleEndian.AppendUint16(cpf, uint16(len(body)))
	cpf = append(cpf, body...)

	return req.Reply(eip.EncapStatusSuccess, cpf)
}

func isClosedNetErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF)
}
