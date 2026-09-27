// Package dnscname reads a name's CNAME record itself, not the end of its
// chain: a custom domain proves it belongs to an app by pointing at that
// app's own hostname, whatever that hostname resolves to further on.
package dnscname

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type Resolver struct {
	// Server is host:port of a recursive resolver.
	Server  string
	Timeout time.Duration
}

// CNAME returns the lowercase target of host's CNAME record, or "" when it has none.
func (r Resolver) CNAME(ctx context.Context, host string) (string, error) {
	name, err := dnsmessage.NewName(strings.TrimSuffix(host, ".") + ".")
	if err != nil {
		return "", fmt.Errorf("invalid name %q: %w", host, err)
	}
	var id [2]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("query id: %w", err)
	}
	query := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: binary.BigEndian.Uint16(id[:]), RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET}},
	}
	reply, err := r.exchange(ctx, query)
	if err != nil {
		return "", err
	}
	return cnameIn(reply, name)
}

func (r Resolver) exchange(ctx context.Context, query dnsmessage.Message) (dnsmessage.Message, error) {
	packed, err := query.Pack()
	if err != nil {
		return dnsmessage.Message{}, fmt.Errorf("pack query: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", r.Server)
	if err != nil {
		return dnsmessage.Message{}, fmt.Errorf("dial resolver: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(packed); err != nil {
		return dnsmessage.Message{}, fmt.Errorf("send query: %w", err)
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return dnsmessage.Message{}, fmt.Errorf("read answer: %w", err)
		}
		var reply dnsmessage.Message
		if reply.Unpack(buf[:n]) == nil && reply.ID == query.ID && reply.Response {
			return reply, nil
		}
	}
}

func cnameIn(reply dnsmessage.Message, name dnsmessage.Name) (string, error) {
	switch reply.RCode {
	case dnsmessage.RCodeSuccess:
	case dnsmessage.RCodeNameError:
		return "", nil
	default:
		return "", fmt.Errorf("resolver answered %s", reply.RCode)
	}
	for _, answer := range reply.Answers {
		cname, ok := answer.Body.(*dnsmessage.CNAMEResource)
		if ok && strings.EqualFold(answer.Header.Name.String(), name.String()) {
			return strings.TrimSuffix(strings.ToLower(cname.CNAME.String()), "."), nil
		}
	}
	return "", nil
}

// SystemServer is the first nameserver the host is configured with.
func SystemServer(resolvConf string) (string, error) {
	file, err := os.Open(resolvConf)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", resolvConf, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			return net.JoinHostPort(fields[1], "53"), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", resolvConf, err)
	}
	return "", errors.New("no nameserver in " + resolvConf)
}
