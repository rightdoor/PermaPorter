package preheat

import (
	"reflect"
	"testing"
)

func TestParseInputGoMod(t *testing.T) {
	src := `module e2e.local/test

go 1.21

require (
	github.com/gin-gonic/gin v1.10.0
	github.com/BurntSushi/toml v1.4.0 // indirect
)

require github.com/dep/one v1.0.0
`
	targets, invalid := ParseInput(src)
	if len(invalid) != 0 {
		t.Fatalf("不应有无效项: %v", invalid)
	}
	want := []Target{
		{Module: "github.com/gin-gonic/gin", Version: "v1.10.0"},
		{Module: "github.com/BurntSushi/toml", Version: "v1.4.0"},
		{Module: "github.com/dep/one", Version: "v1.0.0"},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("解析结果不符: %+v", targets)
	}
}

func TestParseInputGoSum(t *testing.T) {
	src := `github.com/gin-gonic/gin v1.10.0 h1:abc=
github.com/gin-gonic/gin v1.10.0/go.mod h1:def=
github.com/BurntSushi/toml v1.4.0 h1:ghi=
`
	targets, invalid := ParseInput(src)
	if len(invalid) != 0 {
		t.Fatalf("不应有无效项: %v", invalid)
	}
	want := []Target{
		{Module: "github.com/gin-gonic/gin", Version: "v1.10.0"},
		{Module: "github.com/BurntSushi/toml", Version: "v1.4.0"},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("go.sum 解析结果不符（/go.mod 行应去重）: %+v", targets)
	}
}

func TestParseInputPlainText(t *testing.T) {
	src := `# 注释行
github.com/gin-gonic/gin
github.com/gin-gonic/gin@v1.10.0   // 行内注释
gopkg.in/yaml.v3
not a module!
`
	targets, invalid := ParseInput(src)
	if len(invalid) != 1 || invalid[0] != "not a module!" {
		t.Fatalf("无效项应为 1 个: %v", invalid)
	}
	want := []Target{
		{Module: "github.com/gin-gonic/gin"},
		{Module: "github.com/gin-gonic/gin", Version: "v1.10.0"},
		{Module: "gopkg.in/yaml.v3"},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("纯文本解析结果不符: %+v", targets)
	}
}

func TestParseInputEmpty(t *testing.T) {
	if targets, _ := ParseInput("   \n  "); len(targets) != 0 {
		t.Fatalf("空输入应返回空: %+v", targets)
	}
}

func TestParseInputDedupesAndCaps(t *testing.T) {
	src := ""
	for i := 0; i < 600; i++ {
		src += "example.com/mod\n"
	}
	targets, _ := ParseInput(src)
	if len(targets) != 1 {
		t.Fatalf("重复项应去重为 1: %d", len(targets))
	}
}
