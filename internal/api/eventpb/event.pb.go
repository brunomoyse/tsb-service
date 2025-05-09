// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.6
// 	protoc        v5.29.3
// source: event.proto

package eventpb

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type EventMessage struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Type          string                 `protobuf:"bytes,1,opt,name=type,proto3" json:"type,omitempty"`
	Payload       []byte                 `protobuf:"bytes,2,opt,name=payload,proto3" json:"payload,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EventMessage) Reset() {
	*x = EventMessage{}
	mi := &file_event_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EventMessage) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EventMessage) ProtoMessage() {}

func (x *EventMessage) ProtoReflect() protoreflect.Message {
	mi := &file_event_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EventMessage.ProtoReflect.Descriptor instead.
func (*EventMessage) Descriptor() ([]byte, []int) {
	return file_event_proto_rawDescGZIP(), []int{0}
}

func (x *EventMessage) GetType() string {
	if x != nil {
		return x.Type
	}
	return ""
}

func (x *EventMessage) GetPayload() []byte {
	if x != nil {
		return x.Payload
	}
	return nil
}

type Ack struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Status        string                 `protobuf:"bytes,1,opt,name=status,proto3" json:"status,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Ack) Reset() {
	*x = Ack{}
	mi := &file_event_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Ack) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Ack) ProtoMessage() {}

func (x *Ack) ProtoReflect() protoreflect.Message {
	mi := &file_event_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Ack.ProtoReflect.Descriptor instead.
func (*Ack) Descriptor() ([]byte, []int) {
	return file_event_proto_rawDescGZIP(), []int{1}
}

func (x *Ack) GetStatus() string {
	if x != nil {
		return x.Status
	}
	return ""
}

var File_event_proto protoreflect.FileDescriptor

const file_event_proto_rawDesc = "" +
	"\n" +
	"\vevent.proto\x12\x05event\"<\n" +
	"\fEventMessage\x12\x12\n" +
	"\x04type\x18\x01 \x01(\tR\x04type\x12\x18\n" +
	"\apayload\x18\x02 \x01(\fR\apayload\"\x1d\n" +
	"\x03Ack\x12\x16\n" +
	"\x06status\x18\x01 \x01(\tR\x06status2E\n" +
	"\fEventService\x125\n" +
	"\fStreamEvents\x12\x13.event.EventMessage\x1a\n" +
	".event.Ack\"\x00(\x010\x01B*Z(tsb-service/internal/api/eventpb;eventpbb\x06proto3"

var (
	file_event_proto_rawDescOnce sync.Once
	file_event_proto_rawDescData []byte
)

func file_event_proto_rawDescGZIP() []byte {
	file_event_proto_rawDescOnce.Do(func() {
		file_event_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_event_proto_rawDesc), len(file_event_proto_rawDesc)))
	})
	return file_event_proto_rawDescData
}

var file_event_proto_msgTypes = make([]protoimpl.MessageInfo, 2)
var file_event_proto_goTypes = []any{
	(*EventMessage)(nil), // 0: event.EventMessage
	(*Ack)(nil),          // 1: event.Ack
}
var file_event_proto_depIdxs = []int32{
	0, // 0: event.EventService.StreamEvents:input_type -> event.EventMessage
	1, // 1: event.EventService.StreamEvents:output_type -> event.Ack
	1, // [1:2] is the sub-list for method output_type
	0, // [0:1] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_event_proto_init() }
func file_event_proto_init() {
	if File_event_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_event_proto_rawDesc), len(file_event_proto_rawDesc)),
			NumEnums:      0,
			NumMessages:   2,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_event_proto_goTypes,
		DependencyIndexes: file_event_proto_depIdxs,
		MessageInfos:      file_event_proto_msgTypes,
	}.Build()
	File_event_proto = out.File
	file_event_proto_goTypes = nil
	file_event_proto_depIdxs = nil
}
