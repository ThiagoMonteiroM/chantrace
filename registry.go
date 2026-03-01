package chantrace

import (
	"reflect"
	"sync"
)

// chanMeta is stored in the registry sync.Map.
// Callers must use Close or Unregister to remove entries.
type chanMeta struct {
	Name     string
	ElemType string
	Cap      int
}

var registry sync.Map // uintptr → *chanMeta

func chanPtr(ch any) uintptr {
	return reflect.ValueOf(ch).Pointer()
}

func registerChan(ch any, name, elemType string, capacity int) uintptr {
	ptr := chanPtr(ch)
	registry.Store(ptr, &chanMeta{
		Name:     name,
		ElemType: elemType,
		Cap:      capacity,
	})
	return ptr
}

func lookupChan(ch any) (uintptr, *chanMeta) {
	ptr := chanPtr(ch)
	v, ok := registry.Load(ptr)
	if !ok {
		return ptr, nil
	}
	return ptr, v.(*chanMeta)
}

func unregisterChan(ch any) {
	ptr := chanPtr(ch)
	registry.Delete(ptr)
}

// Unregister removes a channel from the trace registry without closing it.
// Use this when a channel goes out of scope but should not be closed
// (e.g., it has active receivers). For channels you own, prefer Close.
func Unregister[T any](ch chan T) {
	unregisterChan(ch)
}

// ChannelInfo describes a registered channel.
type ChannelInfo struct {
	Name     string  `json:"name"`
	ElemType string  `json:"elem_type"`
	Cap      int     `json:"cap"`
	Ptr      uintptr `json:"ptr"`
}

// Channels returns information about all registered channels.
func Channels() []ChannelInfo {
	var infos []ChannelInfo
	registry.Range(func(key, value any) bool {
		ptr := key.(uintptr)
		meta := value.(*chanMeta)
		infos = append(infos, ChannelInfo{
			Name:     meta.Name,
			ElemType: meta.ElemType,
			Cap:      meta.Cap,
			Ptr:      ptr,
		})
		return true
	})
	return infos
}
