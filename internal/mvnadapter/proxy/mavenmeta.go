// maven-metadata.xml 的解析：两级（构件级 / 版本级）共用同一 XML 形态，
// groupId/artifactId 均在 XML 内容中，归属关系不依赖请求路径形状。
package proxy

import (
	"encoding/xml"
	"fmt"
)

// MetadataXML maven-metadata.xml 的结构化形态。
// 版本级（SNAPSHOT）的 <snapshot>/<snapshotVersions> 不参与缓存决策，
// raw XML 透传即协议安全，这里只取记账所需字段。
type MetadataXML struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Versioning struct {
		Latest      string `xml:"latest"`
		Release     string `xml:"release"`
		LastUpdated string `xml:"lastUpdated"`
		Versions    struct {
			Version []string `xml:"version"`
		} `xml:"versions"`
	} `xml:"versioning"`
}

// ParseMetadataXML 解析 maven-metadata.xml。
func ParseMetadataXML(raw []byte) (*MetadataXML, error) {
	var m MetadataXML
	if err := xml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("解析 maven-metadata.xml 失败: %w", err)
	}
	return &m, nil
}

// IsArtifactLevel 是否构件级 metadata（带版本清单）。
// 版本级（SNAPSHOT 映射）没有 <versions>，只做 raw 透传，不更新构件版本信息。
func (m *MetadataXML) IsArtifactLevel() bool { return len(m.Versioning.Versions.Version) > 0 }
