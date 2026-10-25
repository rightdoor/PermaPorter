package handlers

import (
	"reflect"
	"testing"
)

func TestParseDepNamesFromPackageJSON(t *testing.T) {
	raw := `{
		"name": "demo",
		"version": "1.0.0",
		"dependencies": {"vue": "^3.5.0", "axios": "^1.7.0"},
		"devDependencies": {"@babel/core": "^7.0.0", "typescript": "5.x"}
	}`
	names, invalid := parseDepNames(raw)
	want := []string{"@babel/core", "axios", "typescript", "vue"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("解析结果 = %v, 期望 %v", names, want)
	}
	if len(invalid) != 0 {
		t.Fatalf("不应有无效名: %v", invalid)
	}
}

func TestParseDepNamesFromLines(t *testing.T) {
	raw := "vue\naxios@1.7.9\n@scope/pkg@2.0.0\n# 注释\nlodash,\n  react "
	names, invalid := parseDepNames(raw)
	want := []string{"@scope/pkg", "axios", "lodash", "react", "vue"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("解析结果 = %v, 期望 %v", names, want)
	}
	if len(invalid) != 0 {
		t.Fatalf("不应有无效名: %v", invalid)
	}
}

func TestParseDepNamesRejectsInvalid(t *testing.T) {
	names, invalid := parseDepNames("valid-pkg\na/b/c\nbad;name")
	if len(names) != 1 || names[0] != "valid-pkg" {
		t.Fatalf("有效名解析错误: %v", names)
	}
	if len(invalid) != 2 {
		t.Fatalf("应报告 2 个无效名: %v", invalid)
	}
}

func TestParseDepNamesEmpty(t *testing.T) {
	names, _ := parseDepNames("   \n  ")
	if len(names) != 0 {
		t.Fatalf("空输入应返回空: %v", names)
	}
}
