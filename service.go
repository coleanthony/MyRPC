package MyRPC

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

/*
RPC 框架的一个基础能力是：像调用本地程序一样调用远程服务。那如何将程序映射为服务呢？那么对 Go 来说，这个问题就变成了如何将结构体的方法映射为服务。

对 net/rpc 而言，一个函数需要能够被远程调用，需要满足如下五个条件：

the method’s type is exported. – 方法所属类型是导出的。
the method is exported. – 方式是导出的。
the method has two arguments, both exported (or builtin) types. – 两个入参，均为导出或内置类型。
the method’s second argument is a pointer. – 第二个入参必须是一个指针。
the method has return type error. – 返回值为 error 类型。
*/
//func (t *T) MethodName(argType T1, replyType *T2) error

// method：方法本身
// ArgType：第一个参数的类型
// ReplyType：第二个参数的类型
// numCalls：后续统计方法调用次数时会用到
type methodType struct {
	method    reflect.Method
	Argtype   reflect.Type
	Replytype reflect.Type
	numCalls  uint64
}

func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls)
}

// newArgv 和 newReplyv，用于创建对应类型的实例
func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	//argtype可能是指针或者是值
	if m.Argtype.Kind() == reflect.Ptr {
		argv = reflect.New(m.Argtype.Elem())
	} else {
		argv = reflect.New(m.Argtype).Elem()
	}
	return argv
}

func (m *methodType) newRelpyv() reflect.Value {
	//reply 一定要是指针的
	replyv := reflect.New(m.Replytype.Elem())
	switch m.Replytype.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(m.Replytype.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.Replytype.Elem(), 0, 0))
	}
	return replyv
}

// 定义结构体 service
// name 即映射的结构体的名称，比如 T，比如 WaitGroup；
// typ 是结构体的类型；
// receiveval 即结构体的实例本身，保留receiveval是因为在调用时需要receiveval 作为第 0 个参数；
// method 是 map 类型，存储映射的结构体的所有符合条件的方法。
type service struct {
	name       string
	typ        reflect.Type
	receiveval reflect.Value
	method     map[string]*methodType
}

func newService(receiveval interface{}) *service {
	s := new(service)
	s.receiveval = reflect.ValueOf(receiveval)
	s.name = reflect.Indirect(s.receiveval).Type().Name()
	s.typ = reflect.TypeOf(receiveval)
	if !ast.IsExported(s.name) {
		//IsExported reports whether name starts with an upper-case letter
		//rpc里面传的name一定是要首写字母大写
		log.Fatalf("rpc server:%s is not a valid service name", s.name)
	}
	s.registerMethods()
	return s
}

func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		mtype := method.Type
		if mtype.NumIn() != 3 || mtype.NumOut() != 1 {
			//NumIn returns a function type's input parameter count. It panics if the type's Kind is not Func.
			//NumOut returns a function type's output parameter count. It panics if the type's Kind is not Func.
			continue
		}
		if mtype.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		argtype, replytype := mtype.In(1), mtype.In(2)
		if !isExportedOrBuiltinType(argtype) || !isExportedOrBuiltinType(replytype) {
			continue
		}
		s.method[method.Name] = &methodType{
			method:    method,
			Argtype:   argtype,
			Replytype: replytype,
		}
		log.Printf("rpc service: register %s.%s\n", s.name, method.Name)
	}
}

func isExportedOrBuiltinType(t reflect.Type) bool {
	//首字母大写
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

// 实现call方法，通过反射值调用方法
func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	returnValues := f.Call([]reflect.Value{s.receiveval, argv, replyv})
	if err := returnValues[0].Interface(); err != nil {
		return err.(error)
	}
	return nil
}
