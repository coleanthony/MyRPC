package codec

import (
	"io"
)

//codec主要是使用 encoding/gob 实现消息的编解码(序列化与反序列化)
//消息编解码相关的代码

type Header struct {
	ServiceMethod string //rpc method:pkg.method
	Seq           uint64 //请求的序号，也可以认为是某个请求的 ID，用来区分不同的请求
	Error         string //错误信息
}

type Codec interface {
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

type NewCodecFunc func(closer io.ReadWriteCloser) Codec

type Type string

const (
	GobType Type = "application/gob"
	Json    Type = "application/json"
)

var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
