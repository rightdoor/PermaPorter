package handlers

import (
	"testing"
)

// TestParseMavenTargets 预热清单解析：pom.xml 依赖块、Gradle 行、裸坐标行与去重。
func TestParseMavenTargets(t *testing.T) {
	pom := `<project>
  <dependencies>
    <dependency>
      <groupId>com.google.guava</groupId>
      <artifactId>guava</artifactId>
      <version>33.0.0-jre</version>
    </dependency>
    <dependency>
      <groupId>org.example</groupId>
      <artifactId>prop-version</artifactId>
      <version>${project.version}</version>
    </dependency>
    <dependency>
      <groupId>org.example</groupId>
      <artifactId>range-version</artifactId>
      <version>[1.0,2.0)</version>
    </dependency>
  </dependencies>
</project>`
	targets, invalid := parseMavenTargets(pom)
	if len(invalid) != 0 {
		t.Fatalf("pom 输入不应有非法行: %v", invalid)
	}
	if len(targets) != 3 {
		t.Fatalf("应解析 3 个构件: %+v", targets)
	}
	if targets[0].Group != "com.google.guava" || targets[0].Artifact != "guava" || targets[0].Version != "33.0.0-jre" {
		t.Fatalf("具名版本解析不符: %+v", targets[0])
	}
	// ${...} 与区间版本回退空版本（latest）
	if targets[1].Version != "" || targets[2].Version != "" {
		t.Fatalf("不可用版本应回退 latest: %+v", targets)
	}

	gradle := `dependencies {
    implementation "com.squareup.okhttp3:okhttp:4.12.0"
    api('org.slf4j:slf4j-api:2.0.13')
    testImplementation "junit:junit:4.13.2:sources"
    implementation "org.example:plain-dep"
}`
	targets, invalid = parseMavenTargets(gradle)
	if len(invalid) != 0 || len(targets) != 4 {
		t.Fatalf("Gradle 行解析不符: targets=%+v invalid=%v", targets, invalid)
	}
	if targets[0].Group != "com.squareup.okhttp3" || targets[0].Version != "4.12.0" {
		t.Fatalf("引号坐标解析不符: %+v", targets[0])
	}
	// 四段坐标：v + classifier（结果按字母序返回，按名查找）
	var junit *struct{ v, c string }
	for _, t := range targets {
		if t.Artifact == "junit" {
			junit = &struct{ v, c string }{t.Version, t.Classifier}
		}
	}
	if junit == nil || junit.v != "4.13.2" || junit.c != "sources" {
		t.Fatalf("classifier 坐标解析不符: %+v", junit)
	}
	// 无版本坐标
	var plain *struct{ a, v string }
	for _, t := range targets {
		if t.Artifact == "plain-dep" {
			plain = &struct{ a, v string }{t.Artifact, t.Version}
		}
	}
	if plain == nil || plain.a != "plain-dep" || plain.v != "" {
		t.Fatalf("无版本坐标应回退 latest: %+v", plain)
	}

	// 混合粘贴 + 去重 + 噪音行静默跳过 + 坏坐标报错
	mixed := `com.example:demo:1.0.0
com.example:demo:1.0.0
not a coordinate!!
dependencies {
com..bad:demo`
	targets, invalid = parseMavenTargets(mixed)
	if len(targets) != 1 || len(invalid) != 1 || invalid[0] != "com..bad:demo" {
		t.Fatalf("去重/噪音/坏坐标判定不符: targets=%+v invalid=%v", targets, invalid)
	}
}
