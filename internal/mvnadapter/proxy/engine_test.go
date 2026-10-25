package proxy

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/mvnadapter/database"
)

// openTestDB 打开临时单库并挂载 mvn 生态查询层。
func openTestDB(t *testing.T, dir string) *database.DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) +
		"?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"
	store, err := coredatabase.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db := database.Attach(store)
	t.Cleanup(func() { db.Close(); _ = store.Close() })
	return db
}

func fileSHA1(b []byte) string {
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:])
}

func serve(h func(*gin.Context)) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	h(c)
	return w
}

// atomicString 并发安全的字符串（模拟上游 ETag 变化）。
type atomicString struct{ v atomic.Value }

func (s *atomicString) Store(val string) { s.v.Store(val) }
func (s *atomicString) Load() string     { return s.v.Load().(string) }

// TestMetadataAndArtifactFlow 核心链路：构件级 metadata 首次 MISS/二次 HIT、
// 304 只推进时间戳；构件文件按上游 .sha1 校验后落盘、二次请求零拷贝命中；
// .sha1 端点由本地内容计算且与上游一致。
func TestMetadataAndArtifactFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	jar := []byte("demo-jar-bytes-0123456789")
	pom := []byte("<project/>")
	metaXML := []byte(sampleArtifactMeta)
	var etag atomicString
	etag.Store(`"v1"`)
	var upstreamHits int
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		switch {
		case r.URL.Path == "/com/example/demo/maven-metadata.xml":
			if r.Header.Get("If-None-Match") == etag.Load() {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag.Load())
			_, _ = w.Write(metaXML)
		case r.URL.Path == "/com/example/demo/2.0.0/demo-2.0.0.jar":
			w.Header().Set("Content-Length", strconv.Itoa(len(jar)))
			_, _ = w.Write(jar)
		case r.URL.Path == "/com/example/demo/2.0.0/demo-2.0.0.jar.sha1":
			_, _ = w.Write([]byte(fileSHA1(jar) + "  demo-2.0.0.jar"))
		case r.URL.Path == "/com/example/demo/2.0.0/demo-2.0.0.pom":
			_, _ = w.Write(pom)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	const shortTTL = 10 * time.Millisecond
	dir := t.TempDir()
	db := openTestDB(t, dir)
	// 长 TTL 场景：验证 MISS→HIT 与零上游请求；短 TTL 的 304 路径在最后单独验证
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	engine := NewEngine(db, NewStats(db))

	// 冷缓存：metadata 解析入库并原样透传
	w := serve(func(c *gin.Context) { engine.ServeMetadata(c, "/com/example/demo/maven-metadata.xml") })
	if w.Code != http.StatusOK || w.Body.String() != sampleArtifactMeta {
		t.Fatalf("metadata 应原样透传: %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/xml" {
		t.Fatalf("Content-Type 应为 application/xml: %s", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Proxy-Cache") != "MISS" {
		t.Fatalf("首次应为 MISS: %s", w.Header().Get("X-Proxy-Cache"))
	}
	// 坐标与版本清单入库
	art, err := db.GetArtifact("com.example", "demo")
	if err != nil || art.LatestVersion != "2.0.0" || art.VersionCount != 3 || art.LastUpdated != "20240306082314" {
		t.Fatalf("构件记账不符: %+v %v", art, err)
	}

	// 二次请求命中本地缓存（上游零请求）
	before := upstreamHits
	w = serve(func(c *gin.Context) { engine.ServeMetadata(c, "/com/example/demo/maven-metadata.xml") })
	if w.Code != http.StatusOK || w.Header().Get("X-Proxy-Cache") != "HIT" || upstreamHits != before {
		t.Fatalf("二次请求应命中本地: %d cache=%s hits=%d", w.Code, w.Header().Get("X-Proxy-Cache"), upstreamHits-before)
	}

	// TTL 过期 + 上游未变化：条件请求 304，只推进时间戳、不重写缓存
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), shortTTL); err != nil {
		t.Fatalf("缩短 TTL 失败: %v", err)
	}
	time.Sleep(2 * shortTTL)
	genBefore := db.WriteGen()
	w = serve(func(c *gin.Context) { engine.ServeMetadata(c, "/com/example/demo/maven-metadata.xml") })
	if w.Code != http.StatusOK || w.Body.String() != sampleArtifactMeta {
		t.Fatalf("304 后应继续返回旧内容: %d", w.Code)
	}
	if genAfter := db.WriteGen(); genAfter != genBefore {
		t.Fatalf("304 路径不应推进写代次: before=%d after=%d", genBefore, genAfter)
	}

	// 构件文件：冷缓存回源（sha1 校验链）后落盘镜像仓库布局
	w = serve(func(c *gin.Context) { engine.ServeArtifactFile(c, "/com/example/demo/2.0.0/demo-2.0.0.jar") })
	if w.Code != http.StatusOK || w.Body.String() != string(jar) {
		t.Fatalf("构件应下载成功: %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Proxy-Cache") != "MISS" {
		t.Fatalf("首次构件请求应为 MISS: %s", w.Header().Get("X-Proxy-Cache"))
	}
	if _, err := os.Stat(filepath.Join(dir, "storage", "com", "example", "demo", "2.0.0", "demo-2.0.0.jar")); err != nil {
		t.Fatalf("构件应镜像仓库布局落盘: %v", err)
	}
	rec, err := db.GetFileByRepoPath("com/example/demo/2.0.0/demo-2.0.0.jar")
	if err != nil || rec.FileSizeBytes != int64(len(jar)) || rec.SHA1 != fileSHA1(jar) {
		t.Fatalf("文件记录不符: %+v %v", rec, err)
	}

	// 二次请求零拷贝命中
	before = upstreamHits
	w = serve(func(c *gin.Context) { engine.ServeArtifactFile(c, "/com/example/demo/2.0.0/demo-2.0.0.jar") })
	if w.Code != http.StatusOK || w.Header().Get("X-Proxy-Cache") != "HIT" || upstreamHits != before {
		t.Fatalf("二次构件请求应命中本地: %d", w.Code)
	}

	// .sha1 端点：由本地内容即时计算，与上游校验文件一致
	w = serve(func(c *gin.Context) { engine.ServeChecksum(c, "/com/example/demo/2.0.0/demo-2.0.0.jar.sha1") })
	if w.Code != http.StatusOK || w.Body.String() != fileSHA1(jar) {
		t.Fatalf("sha1 端点不符: %d %q", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("sha1 端点应为 text/plain: %s", w.Header().Get("Content-Type"))
	}
	// metadata 的校验和端点同样可算
	w = serve(func(c *gin.Context) { engine.ServeChecksum(c, "/com/example/demo/maven-metadata.xml.sha1") })
	if w.Code != http.StatusOK || w.Body.String() != fileSHA1([]byte(sampleArtifactMeta)) {
		t.Fatalf("metadata sha1 端点不符: %d %q", w.Code, w.Body.String())
	}
}

// TestArtifactSHA1MismatchRejected 校验链：上游 .sha1 与内容不符时必须丢弃、
// 不得落盘不得入库（返回 502），修复后（模拟备用上游提供正确内容）可成功。
func TestArtifactSHA1MismatchRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jar := []byte("corrupted-source")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/com/example/demo/1.0.0/demo-1.0.0.jar":
			_, _ = w.Write(jar)
		case r.URL.Path == "/com/example/demo/1.0.0/demo-1.0.0.jar.sha1":
			_, _ = w.Write([]byte(strings.Repeat("0", 40)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(db, NewStats(db))

	w := serve(func(c *gin.Context) { engine.ServeArtifactFile(c, "/com/example/demo/1.0.0/demo-1.0.0.jar") })
	if w.Code != http.StatusBadGateway {
		t.Fatalf("sha1 不符应 502，实际 %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "storage", "com", "example", "demo", "1.0.0", "demo-1.0.0.jar")); !os.IsNotExist(err) {
		t.Fatalf("校验失败的构件不得落盘: %v", err)
	}
	if _, err := db.GetFileByRepoPath("com/example/demo/1.0.0/demo-1.0.0.jar"); err == nil {
		t.Fatal("校验失败的构件不得入库")
	}
}

// TestMetadataAndArtifactNotFound 上游 404：metadata 与构件均返回 text/plain 404。
func TestMetadataAndArtifactNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(db, NewStats(db))

	for _, h := range []struct {
		name string
		fn   func(*gin.Context)
	}{
		{"meta", func(c *gin.Context) { engine.ServeMetadata(c, "/com/example/demo/maven-metadata.xml") }},
		{"file", func(c *gin.Context) { engine.ServeArtifactFile(c, "/com/example/demo/1.0.0/demo-1.0.0.jar") }},
	} {
		w := serve(h.fn)
		if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s 上游 404 应透传 text/plain 404: %d %s", h.name, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

// TestRejectsInvalidPaths 非法路径直接 400，不触达上游。
func TestRejectsInvalidPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings("http://127.0.0.1:1", filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(db, NewStats(db))

	for _, h := range []struct {
		name string
		fn   func(*gin.Context)
	}{
		{"meta 穿越", func(c *gin.Context) { engine.ServeMetadata(c, "/com/../etc/maven-metadata.xml") }},
		{"file 穿越", func(c *gin.Context) { engine.ServeArtifactFile(c, "/com/example/demo/1.0.0/../../demo-1.0.0.jar") }},
		{"file 布局不符", func(c *gin.Context) { engine.ServeArtifactFile(c, "/com/example/demo/1.0.0/other-file.jar") }},
	} {
		w := serve(h.fn)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d", h.name, w.Code)
		}
	}
}

// TestPreheatArtifact 预热：解析 metadata 得 latest/release，下载 pom 与 jar；
// 重复预热零下载返回 cached。
func TestPreheatArtifact(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jar := []byte("preheat-jar-content")
	pom := []byte("<project>preheat</project>")
	var downloads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/com/example/demo/maven-metadata.xml":
			_, _ = w.Write([]byte(sampleArtifactMeta))
		case r.URL.Path == "/com/example/demo/2.0.0/demo-2.0.0.jar":
			downloads.Add(1)
			_, _ = w.Write(jar)
		case r.URL.Path == "/com/example/demo/2.0.0/demo-2.0.0.jar.sha1":
			_, _ = w.Write([]byte(fileSHA1(jar)))
		case r.URL.Path == "/com/example/demo/2.0.0/demo-2.0.0.pom":
			downloads.Add(1)
			_, _ = w.Write(pom)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(db, NewStats(db))

	state, ver, size, err := engine.PreheatArtifact("com.example", "demo", "", "", true)
	if err != nil {
		t.Fatalf("首次预热失败: %v", err)
	}
	if state != "done" || ver != "2.0.0" {
		t.Fatalf("首次预热 state/version 异常: %s %s", state, ver)
	}
	if size != int64(len(jar)+len(pom)) {
		t.Fatalf("体积统计不符: %d", size)
	}
	if n := downloads.Load(); n != 2 {
		t.Fatalf("首次预热应下载 jar+pom 共 2 次，实际 %d", n)
	}

	// 重复预热：全部命中，零下载
	state, _, size2, err := engine.PreheatArtifact("com.example", "demo", "", "", true)
	if err != nil || state != "cached" {
		t.Fatalf("重复预热应返回 cached: %s %v", state, err)
	}
	if size2 != int64(len(jar)+len(pom)) {
		t.Fatalf("重复预热体积统计应稳定: %d", size2)
	}
	if n := downloads.Load(); n != 2 {
		t.Fatalf("重复预热不得再次下载（应仍为 2 次，实际 %d）", n)
	}

	// 仅元数据预热：已有缓存直接返回 cached、零下载
	state, _, _, err = engine.PreheatArtifact("com.example", "demo", "", "", false)
	if err != nil || state != "cached" {
		t.Fatalf("仅元数据预热应 cached: %s %v", state, err)
	}
}

// TestConcurrentDownloadsNoGateDeadlock 回归测试：downloadArtifact 持上游并发闸期间
// 不得嵌套获取同一闸门（嵌套 acquire 在并发打满闸容量时互相等待死锁，如预取 .sha1 场景）。
// 构造超过闸容量（16）的并发下载，全部必须在限时内完成。
func TestConcurrentDownloadsNoGateDeadlock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const n = upstreamMaxConcurrent + 8 // 超过闸容量，确保曾经会占满全部闸位
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 慢上游放大竞态窗口：让外层下载充分占闸后再发起 .sha1 预取
		time.Sleep(30 * time.Millisecond)
		if strings.HasSuffix(r.URL.Path, "/maven-metadata.xml") {
			_, _ = w.Write([]byte(`<metadata><groupId>x</groupId><artifactId>y</artifactId></metadata>`))
			return
		}
		if strings.HasSuffix(r.URL.Path, ".sha1") {
			// 上游不提供校验文件：走"无期望值"分支，不影响下载路径
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("artifact-bytes"))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(db, NewStats(db))

	// 先串行缓存全部元数据：让并发阶段的 goroutine 全部直奔下载路径，
	// 同一瞬间争抢闸位（元数据阶段的闸门排队会天然错峰，掩盖死锁形态）
	groups := make([]string, n)
	for i := range groups {
		groups[i] = fmt.Sprintf("com.example.g%d", i)
		if _, _, _, err := engine.PreheatArtifact(groups[i], "demo", "1.0.0", "", false); err != nil {
			t.Fatalf("元数据预热失败: %v", err)
		}
	}

	done := make(chan error, n)
	for _, g := range groups {
		go func(group string) {
			_, _, _, err := engine.PreheatArtifact(group, "demo", "1.0.0", "", true)
			done <- err
		}(g)
	}
	// 死锁形态下 16 个外层持闸者互相等待，永远等不到结果：限时失败而不是挂死测试
	var firstErr error
	timeout := time.After(30 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case err := <-done:
			if err != nil && firstErr == nil {
				firstErr = err
			}
		case <-timeout:
			t.Fatalf("并发下载超时：疑似上游并发闸嵌套死锁（已完成 %d/%d）", i, n)
		}
	}
	if firstErr != nil {
		t.Fatalf("并发下载失败: %v", firstErr)
	}
}

// TestPreheatDoesNotPolluteStats 回归测试：预热不得计入请求命中率统计——
// 命中率只属于协议请求路径（ServeArtifactFile/ServeChecksum），与 py 侧口径一致。
func TestPreheatDoesNotPolluteStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	jar := []byte("stat-jar")
	pom := []byte("<project/>")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/com/example/demo/maven-metadata.xml":
			_, _ = w.Write([]byte(sampleArtifactMeta))
		case r.URL.Path == "/com/example/demo/2.0.0/demo-2.0.0.jar":
			_, _ = w.Write(jar)
		case r.URL.Path == "/com/example/demo/2.0.0/demo-2.0.0.pom":
			_, _ = w.Write(pom)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(db, NewStats(db))

	if _, _, _, err := engine.PreheatArtifact("com.example", "demo", "", "", true); err != nil {
		t.Fatalf("预热失败: %v", err)
	}
	st, err := db.GetStatusStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.MetaTotal != 0 || st.MetaHits != 0 || st.FileTotal != 0 || st.FileHits != 0 {
		t.Fatalf("预热不应计入请求统计: %+v", st)
	}

	// 协议请求路径正常计数（区分冷/热）
	w := serve(func(c *gin.Context) { engine.ServeArtifactFile(c, "/com/example/demo/2.0.0/demo-2.0.0.jar") })
	if w.Code != http.StatusOK {
		t.Fatalf("协议请求应成功: %d", w.Code)
	}
	st2, _ := db.GetStatusStats()
	if st2.FileTotal != 1 || st2.FileHits != 1 {
		t.Fatalf("协议请求应计入命中统计: %+v", st2)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
