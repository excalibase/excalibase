package docbrowser

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const opMsg = 2013

// wireFake is a MongoDB wire-protocol server small enough to answer the
// commands the store sends, so the store's success paths run in every test
// build rather than only against a real gateway. It records each command's
// name, which also shows the store sends nothing it was not written to.
type wireFake struct {
	listener net.Listener
	mu       sync.Mutex
	commands []string
}

func startWireFake(t *testing.T) *wireFake {
	t.Helper()
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &wireFake{listener: listener}
	go fake.serve()
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Logf("close fake: %v", err)
		}
	})
	return fake
}

func (f *wireFake) address() string { return f.listener.Addr().String() }

func (f *wireFake) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func (f *wireFake) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *wireFake) handle(conn net.Conn) {
	defer conn.Close()
	for {
		requestID, command, err := readOpMsg(conn)
		if err != nil {
			return
		}
		name := command.Index(0).Key()
		f.mu.Lock()
		f.commands = append(f.commands, name)
		f.mu.Unlock()
		if writeOpMsg(conn, requestID, replyTo(name, command)) != nil {
			return
		}
	}
}

func readOpMsg(r io.Reader) (int32, bson.Raw, error) {
	header := make([]byte, 16)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	length := int32(binary.LittleEndian.Uint32(header[0:4]))
	body := make([]byte, length-16)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	if binary.LittleEndian.Uint32(header[12:16]) != opMsg || body[4] != 0 {
		return 0, nil, errors.New("not an OP_MSG with a body first")
	}
	docLength := binary.LittleEndian.Uint32(body[5:9])
	return int32(binary.LittleEndian.Uint32(header[4:8])), bson.Raw(body[5 : 5+docLength]), nil
}

func writeOpMsg(w io.Writer, responseTo int32, reply bson.D) error {
	doc, err := bson.Marshal(reply)
	if err != nil {
		return err
	}
	message := make([]byte, 21, 21+len(doc))
	binary.LittleEndian.PutUint32(message[0:4], uint32(21+len(doc)))
	binary.LittleEndian.PutUint32(message[8:12], uint32(responseTo))
	binary.LittleEndian.PutUint32(message[12:16], opMsg)
	_, err = w.Write(append(message, doc...))
	return err
}

func cursorReply(ns string, docs ...bson.D) bson.D {
	batch := bson.A{}
	for _, doc := range docs {
		batch = append(batch, doc)
	}
	return bson.D{
		{Key: "cursor", Value: bson.D{{Key: "firstBatch", Value: batch}, {Key: "id", Value: int64(0)}, {Key: "ns", Value: ns}}},
		{Key: "ok", Value: 1.0},
	}
}

func replyTo(name string, command bson.Raw) bson.D {
	ok := bson.D{{Key: "ok", Value: 1.0}}
	switch name {
	case "hello", "isMaster", "ismaster":
		return bson.D{
			{Key: "isWritablePrimary", Value: true}, {Key: "maxWireVersion", Value: int32(21)},
			{Key: "minWireVersion", Value: int32(0)}, {Key: "maxBsonObjectSize", Value: int32(16 << 20)},
			{Key: "maxMessageSizeBytes", Value: int32(48 << 20)}, {Key: "maxWriteBatchSize", Value: int32(100000)},
			{Key: "ok", Value: 1.0},
		}
	case "listDatabases":
		return bson.D{{Key: "databases", Value: bson.A{bson.D{{Key: "name", Value: "shop"}}}}, {Key: "ok", Value: 1.0}}
	case "listCollections":
		return cursorReply("shop.$cmd.listCollections", bson.D{{Key: "name", Value: "orders"}, {Key: "type", Value: "collection"}})
	case "find":
		return cursorReply("shop.orders", bson.D{{Key: "_id", Value: int32(1)}})
	case "listIndexes":
		return cursorReply("shop.orders", bson.D{{Key: "name", Value: "_id_"}})
	case "aggregate":
		return aggregateReply(command)
	case "insert", "delete":
		return bson.D{{Key: "n", Value: int32(1)}, {Key: "ok", Value: 1.0}}
	case "update":
		return bson.D{{Key: "n", Value: int32(1)}, {Key: "nModified", Value: int32(1)}, {Key: "ok", Value: 1.0}}
	}
	return ok
}

// aggregateReply answers the two pipelines the store runs: CountDocuments'
// $group and the field-inference $sample.
func aggregateReply(command bson.Raw) bson.D {
	first := command.Lookup("pipeline").Array().Index(0).Document().Index(0).Key()
	if first == "$sample" {
		return cursorReply("shop.orders", bson.D{{Key: "a", Value: int32(1)}}, bson.D{{Key: "a", Value: int32(2)}})
	}
	return cursorReply("shop.orders", bson.D{{Key: "_id", Value: int32(1)}, {Key: "n", Value: int32(2)}})
}
