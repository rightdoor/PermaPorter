package proxy

import (
	"testing"
)

const sampleArtifactMeta = `<?xml version="1.0" encoding="UTF-8"?>
<metadata>
  <groupId>com.example</groupId>
  <artifactId>demo</artifactId>
  <versioning>
    <latest>2.0.0</latest>
    <release>2.0.0</release>
    <versions>
      <version>1.0.0</version>
      <version>1.1.0</version>
      <version>2.0.0</version>
    </versions>
    <lastUpdated>20240306082314</lastUpdated>
  </versioning>
</metadata>`

const sampleVersionMeta = `<?xml version="1.0" encoding="UTF-8"?>
<metadata>
  <groupId>com.example</groupId>
  <artifactId>demo</artifactId>
  <version>1.0-SNAPSHOT</version>
  <versioning>
    <snapshot>
      <timestamp>20240306.082314</timestamp>
      <buildNumber>42</buildNumber>
    </snapshot>
    <snapshotVersions>
      <snapshotVersion>
        <extension>jar</extension>
        <value>1.0-20240306.082314-42</value>
        <updated>20240306082314</updated>
      </snapshotVersion>
    </snapshotVersions>
    <lastUpdated>20240306082314</lastUpdated>
  </versioning>
</metadata>`

// TestParseMetadataXMLArtifactLevel 构件级 metadata：坐标与版本清单。
func TestParseMetadataXMLArtifactLevel(t *testing.T) {
	m, err := ParseMetadataXML([]byte(sampleArtifactMeta))
	if err != nil {
		t.Fatal(err)
	}
	if m.GroupID != "com.example" || m.ArtifactID != "demo" {
		t.Fatalf("坐标不符: %+v", m)
	}
	if !m.IsArtifactLevel() {
		t.Fatal("带 <versions> 应判定为构件级")
	}
	if m.Versioning.Latest != "2.0.0" || m.Versioning.Release != "2.0.0" {
		t.Fatalf("latest/release 不符: %+v", m.Versioning)
	}
	if len(m.Versioning.Versions.Version) != 3 || m.Versioning.LastUpdated != "20240306082314" {
		t.Fatalf("版本清单不符: %+v", m.Versioning)
	}
}

// TestParseMetadataXMLVersionLevel 版本级（SNAPSHOT）metadata：无版本清单，不是构件级。
func TestParseMetadataXMLVersionLevel(t *testing.T) {
	m, err := ParseMetadataXML([]byte(sampleVersionMeta))
	if err != nil {
		t.Fatal(err)
	}
	if m.GroupID != "com.example" || m.ArtifactID != "demo" {
		t.Fatalf("坐标不符: %+v", m)
	}
	if m.IsArtifactLevel() {
		t.Fatal("无 <versions> 应判定为版本级")
	}
}

// TestParseMetadataXMLRejectsGarbage 非法 XML 必须报错（不得入库半截内容）。
func TestParseMetadataXMLRejectsGarbage(t *testing.T) {
	if _, err := ParseMetadataXML([]byte("not xml at all")); err == nil {
		t.Fatal("垃圾内容应报错")
	}
}
