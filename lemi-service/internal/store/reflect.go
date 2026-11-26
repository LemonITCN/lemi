package store

import (
	"errors"
	"reflect"
	"strings"
	"sync"
)

var colCache sync.Map // reflect.Type -> []string

// columnsOf 返回结构体（或其指针）带 db 标签的字段名，跳过 db:"-" 与匿名嵌入以外的非导出字段。
func columnsOf(v any) ([]string, error) {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, errors.New("store: 需要结构体")
	}
	if c, ok := colCache.Load(t); ok {
		return c.([]string), nil
	}
	var cols []string
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(f.Type)
				continue
			}
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("db")
			if tag == "" || tag == "-" {
				continue
			}
			cols = append(cols, strings.Split(tag, ",")[0])
		}
	}
	walk(t)
	colCache.Store(t, cols)
	return cols, nil
}
