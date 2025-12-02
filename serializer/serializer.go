package serializer

import (
	"encoding/json"
	"errors"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var Default Serializer = &ProtobufSerializer{}

var Json Serializer = &JsonSerializer{}
var Protobuf Serializer = &ProtobufSerializer{}

// Serializer 序列化反序列化器
type Serializer interface {
	Unmarshal(data []byte, v any) error
	Marshal(v any) ([]byte, error)
}

type ProtobufSerializer struct{}

func (_ *ProtobufSerializer) Unmarshal(data []byte, v any) error {
	message, ok := v.(protoreflect.ProtoMessage)
	if !ok {
		return errors.New("v 不是 proto.Message")
	}
	return proto.Unmarshal(data, message)
}
func (_ *ProtobufSerializer) Marshal(v any) ([]byte, error) {
	message, ok := v.(protoreflect.ProtoMessage)
	if !ok {
		return nil, errors.New("v 不是 proto.Message")
	}
	return proto.Marshal(message)
}

type JsonSerializer struct{}

func (_ *JsonSerializer) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
func (_ *JsonSerializer) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

const (
	KindProto = 0 //protobuf
	KindJson  = 1 //json
)

func GetSerializer(kind int) Serializer {
	if kind == KindJson {
		return Json
	} else {
		return Protobuf
	}
}
