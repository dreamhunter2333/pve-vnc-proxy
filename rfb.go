package main

import (
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	rfbVersion       = "RFB 003.008\n"
	rfbVersionPrefix = "RFB 003."

	secTypeVeNCrypt = 19
	secTypeVNCAuth  = 2

	vencryptPlain = 256

	maxReasonLen = 4096
	maxCredLen   = 1024
)

type routeInfo struct {
	Node    string
	VMID    int
	TokenID string
	Secret  string
}

func handshakeClient(c io.ReadWriter) (*routeInfo, error) {
	if _, err := c.Write([]byte(rfbVersion)); err != nil {
		return nil, fmt.Errorf("send version: %w", err)
	}
	ver := make([]byte, 12)
	if _, err := io.ReadFull(c, ver); err != nil {
		return nil, fmt.Errorf("read client version: %w", err)
	}
	if !strings.HasPrefix(string(ver), rfbVersionPrefix) {
		return nil, fmt.Errorf("unsupported client version %q", string(ver))
	}

	if _, err := c.Write([]byte{1, secTypeVeNCrypt}); err != nil {
		return nil, err
	}
	pick := make([]byte, 1)
	if _, err := io.ReadFull(c, pick); err != nil {
		return nil, err
	}
	if pick[0] != secTypeVeNCrypt {
		return nil, fmt.Errorf("client picked unsupported security %d", pick[0])
	}

	if _, err := c.Write([]byte{0, 2}); err != nil {
		return nil, err
	}
	cver := make([]byte, 2)
	if _, err := io.ReadFull(c, cver); err != nil {
		return nil, err
	}
	if cver[0] != 0 || cver[1] < 2 {
		return nil, fmt.Errorf("unsupported vencrypt version %d.%d", cver[0], cver[1])
	}
	if _, err := c.Write([]byte{0}); err != nil {
		return nil, err
	}

	subList := []byte{1, 0, 0, 1, 0}
	if _, err := c.Write(subList); err != nil {
		return nil, err
	}
	subPick := make([]byte, 4)
	if _, err := io.ReadFull(c, subPick); err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint32(subPick) != vencryptPlain {
		return nil, fmt.Errorf("client picked unsupported vencrypt subtype %d", binary.BigEndian.Uint32(subPick))
	}

	lens := make([]byte, 8)
	if _, err := io.ReadFull(c, lens); err != nil {
		return nil, fmt.Errorf("read plain lens: %w", err)
	}
	ulen := binary.BigEndian.Uint32(lens[0:4])
	plen := binary.BigEndian.Uint32(lens[4:8])
	if ulen > maxCredLen || plen > maxCredLen {
		return nil, fmt.Errorf("plain creds too long: u=%d p=%d", ulen, plen)
	}
	buf := make([]byte, ulen+plen)
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, fmt.Errorf("read plain creds: %w", err)
	}
	user := string(buf[:ulen])
	pass := string(buf[ulen:])

	at1 := strings.Index(user, "@")
	if at1 < 0 {
		writeSecResult(c, "username must be node@vmid@token-id")
		return nil, errors.New("bad username format")
	}
	node := user[:at1]
	rest := user[at1+1:]
	at2 := strings.Index(rest, "@")
	if at2 < 0 {
		writeSecResult(c, "username must be node@vmid@token-id")
		return nil, errors.New("missing token-id")
	}
	vmid, err := strconv.Atoi(rest[:at2])
	if err != nil || vmid <= 0 {
		writeSecResult(c, "vmid must be integer")
		return nil, fmt.Errorf("bad vmid: %w", err)
	}
	tokenID := rest[at2+1:]
	if tokenID == "" {
		writeSecResult(c, "empty token-id")
		return nil, errors.New("empty token-id")
	}
	if pass == "" {
		writeSecResult(c, "empty token secret")
		return nil, errors.New("empty secret")
	}
	return &routeInfo{Node: node, VMID: vmid, TokenID: tokenID, Secret: pass}, nil
}

func finishClientAuthOK(c io.Writer) error {
	_, err := c.Write([]byte{0, 0, 0, 0})
	return err
}

func writeSecResult(c io.Writer, msg string) error {
	if _, err := c.Write([]byte{0, 0, 0, 1}); err != nil {
		return err
	}
	reason := []byte(msg)
	if len(reason) > maxReasonLen {
		reason = reason[:maxReasonLen]
	}
	rl := make([]byte, 4)
	binary.BigEndian.PutUint32(rl, uint32(len(reason)))
	if _, err := c.Write(rl); err != nil {
		return err
	}
	_, err := c.Write(reason)
	return err
}

func handshakeUpstream(u io.ReadWriter, ticket string) error {
	ver := make([]byte, 12)
	if _, err := io.ReadFull(u, ver); err != nil {
		return fmt.Errorf("read upstream version: %w", err)
	}
	if !strings.HasPrefix(string(ver), rfbVersionPrefix) {
		return fmt.Errorf("unsupported upstream version %q", string(ver))
	}
	if _, err := u.Write([]byte(rfbVersion)); err != nil {
		return err
	}

	cnt := make([]byte, 1)
	if _, err := io.ReadFull(u, cnt); err != nil {
		return fmt.Errorf("read sec count: %w", err)
	}
	if cnt[0] == 0 {
		return fmt.Errorf("upstream refused: %s", readBoundedReason(u))
	}
	types := make([]byte, cnt[0])
	if _, err := io.ReadFull(u, types); err != nil {
		return err
	}
	hasVNCAuth := false
	for _, t := range types {
		if t == secTypeVNCAuth {
			hasVNCAuth = true
			break
		}
	}
	if !hasVNCAuth {
		return fmt.Errorf("upstream no VNCAuth, got %v", types)
	}
	if _, err := u.Write([]byte{secTypeVNCAuth}); err != nil {
		return err
	}

	challenge := make([]byte, 16)
	if _, err := io.ReadFull(u, challenge); err != nil {
		return fmt.Errorf("read challenge: %w", err)
	}
	resp, err := vncEncrypt(ticket, challenge)
	if err != nil {
		return err
	}
	if _, err := u.Write(resp); err != nil {
		return err
	}

	secRes := make([]byte, 4)
	if _, err := io.ReadFull(u, secRes); err != nil {
		return fmt.Errorf("read sec result: %w", err)
	}
	if binary.BigEndian.Uint32(secRes) != 0 {
		return fmt.Errorf("upstream auth failed: %s", readBoundedReason(u))
	}
	return nil
}

func readBoundedReason(u io.Reader) string {
	rl := make([]byte, 4)
	if _, err := io.ReadFull(u, rl); err != nil {
		return "<unreadable>"
	}
	n := binary.BigEndian.Uint32(rl)
	if n > maxReasonLen {
		n = maxReasonLen
	}
	reason := make([]byte, n)
	if _, err := io.ReadFull(u, reason); err != nil {
		return "<truncated>"
	}
	return string(reason)
}

func vncEncrypt(password string, challenge []byte) ([]byte, error) {
	key := make([]byte, 8)
	pw := []byte(password)
	for i := 0; i < 8 && i < len(pw); i++ {
		key[i] = bitReverse(pw[i])
	}
	cipher, err := des.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	cipher.Encrypt(out[0:8], challenge[0:8])
	cipher.Encrypt(out[8:16], challenge[8:16])
	return out, nil
}

func bitReverse(b byte) byte {
	b = (b&0xF0)>>4 | (b&0x0F)<<4
	b = (b&0xCC)>>2 | (b&0x33)<<2
	b = (b&0xAA)>>1 | (b&0x55)<<1
	return b
}
