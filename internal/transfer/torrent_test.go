package transfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func buildTestMultiFileTorrent(t *testing.T, rootDir, packName string, files map[string][]byte) (string, *metainfo.MetaInfo) {
	t.Helper()

	packDir := filepath.Join(rootDir, packName)
	for rel, content := range files {
		full := filepath.Join(packDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("创建测试数据目录失败: %v", err)
		}
		if err := os.WriteFile(full, content, 0644); err != nil {
			t.Fatalf("写入测试数据文件失败: %v", err)
		}
	}

	info := metainfo.Info{
		PieceLength: 16 * 1024, // 刻意使用 16KB 小分片，覆盖跨文件分片边界清理逻辑
	}
	if err := info.BuildFromFilePath(packDir); err != nil {
		t.Fatalf("构建 torrent Info 失败: %v", err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("序列化 torrent Info 失败: %v", err)
	}

	mi := &metainfo.MetaInfo{
		InfoBytes: infoBytes,
		CreatedBy: "kkpan-test",
	}
	mi.SetDefaults()

	torrentPath := filepath.Join(rootDir, packName+".torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatalf("创建测试种子文件失败: %v", err)
	}
	if err := mi.Write(f); err != nil {
		_ = f.Close()
		t.Fatalf("写入测试种子文件失败: %v", err)
	}
	_ = f.Close()
	return torrentPath, mi
}

func startLocalTestSeeder(t *testing.T, seedBaseDir string, mi *metainfo.MetaInfo) (*torrent.Client, string) {
	t.Helper()

	discardLogger := slog.New(slog.DiscardHandler)
	stor := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   seedBaseDir,
		PieceCompletion: storage.NewMapPieceCompletion(),
		DisableMmap:     true,
		Logger:          discardLogger,
	})

	cc := torrent.NewDefaultClientConfig()
	cc.DefaultStorage = stor
	cc.DataDir = seedBaseDir
	cc.ListenHost = func(string) string { return "127.0.0.1" }
	cc.ListenPort = 0
	cc.Seed = true
	cc.DisableIPv6 = true
	cc.NoDHT = true
	cc.DisableTrackers = true
	cc.DisableUTP = true
	cc.DisablePEX = true
	cc.NoDefaultPortForwarding = true
	cc.Slogger = discardLogger

	cl, err := torrent.NewClient(cc)
	if err != nil {
		_ = stor.Close()
		t.Fatalf("启动本地 Seeder 失败: %v", err)
	}
	t.Cleanup(func() {
		cl.Close()
		_ = stor.Close()
	})

	tr, err := cl.AddTorrent(mi)
	if err != nil {
		t.Fatalf("Seeder 加载种子失败: %v", err)
	}
	if err := tr.VerifyData(); err != nil {
		t.Fatalf("Seeder 校验本地数据失败: %v", err)
	}
	tr.AllowDataUpload()

	addr := fmt.Sprintf("127.0.0.1:%d", cl.LocalPort())
	return cl, addr
}

func TestTorrentSourceParsing(t *testing.T) {
	tmpDir := t.TempDir()
	localTorrent := filepath.Join(tmpDir, "demo.torrent")
	if err := os.WriteFile(localTorrent, []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}

	s1, err := ParseTorrentSource(localTorrent)
	if err != nil || s1.Kind != SourceLocalFile {
		t.Fatalf("期望识别为本地文件，实际: src=%+v err=%v", s1, err)
	}

	s2, err := ParseTorrentSource("我分享了一个资源 magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=test 点击查看")
	if err != nil || s2.Kind != SourceMagnet || !strings.HasPrefix(s2.Normalized, "magnet:?xt=urn:btih:") {
		t.Fatalf("期望从混合文本中提取磁力链接，实际: src=%+v err=%v", s2, err)
	}

	s3, err := ParseTorrentSource("0123456789ABCDEF0123456789ABCDEF01234567")
	if err != nil || s3.Kind != SourceMagnet || s3.Normalized != "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("期望识别裸 40 位 hex InfoHash，实际: src=%+v err=%v", s3, err)
	}

	s4, err := ParseTorrentSource("MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U")
	if err != nil || s4.Kind != SourceMagnet {
		t.Fatalf("期望识别裸 32 位 base32 InfoHash，实际: src=%+v err=%v", s4, err)
	}

	s5, err := ParseTorrentSource("https://example.com/test.torrent")
	if err != nil || s5.Kind != SourceRemoteURL {
		t.Fatalf("期望识别远程 URL，实际: src=%+v err=%v", s5, err)
	}

	if _, err := ParseTorrentSource("not-a-valid-source"); err == nil {
		t.Fatalf("期望无效输入报错")
	}
	if _, err := ParseTorrentSource(tmpDir); err == nil {
		t.Fatalf("期望传入目录时报错")
	}
}

func TestTorrentFileSelection(t *testing.T) {
	files := []TorrentFileEntry{
		{Index: 1, Path: "01_intro.mp4", DisplayPath: "pack/01_intro.mp4", Size: 100},
		{Index: 2, Path: "subs/01_intro.srt", DisplayPath: "pack/subs/01_intro.srt", Size: 20},
		{Index: 3, Path: "02_main.MKV", DisplayPath: "pack/02_main.MKV", Size: 500},
		{Index: 4, Path: "bonus/readme.txt", DisplayPath: "pack/bonus/readme.txt", Size: 10},
	}

	idx, err := ParseFileIndexExpr("3,1-2,2", len(files))
	if err != nil {
		t.Fatalf("ParseFileIndexExpr 失败: %v", err)
	}
	if len(idx) != 3 || idx[0] != 3 || idx[1] != 1 || idx[2] != 2 {
		t.Fatalf("期望去重保序 [3 1 2]，实际: %+v", idx)
	}

	for _, bad := range []string{"0", "5", "3-1", "abc", "1-2-3"} {
		if _, err := ParseFileIndexExpr(bad, len(files)); err == nil {
			t.Fatalf("期望非法序号表达式 %q 报错", bad)
		}
	}

	// 通配符 + 大小写不敏感
	sel1, err := SelectTorrentFiles(files, "", "*.mp4,*.mkv")
	if err != nil || len(sel1) != 2 || sel1[0].Index != 1 || sel1[1].Index != 3 {
		t.Fatalf("通配符筛选异常: sel=%+v err=%v", sel1, err)
	}

	// 序号 + 关键词取交集
	sel2, err := SelectTorrentFiles(files, "1-3", "intro")
	if err != nil || len(sel2) != 2 || sel2[0].Index != 1 || sel2[1].Index != 2 {
		t.Fatalf("交集筛选异常: sel=%+v err=%v", sel2, err)
	}

	// 空结果报错
	if _, err := SelectTorrentFiles(files, "4", "*.mp4"); err == nil {
		t.Fatalf("期望空交集筛选返回错误")
	}
}

func TestTorrentPathTraversalProtection(t *testing.T) {
	info := metainfo.Info{
		Name:        "evil-pack",
		PieceLength: 16384,
		Pieces:      make([]byte, 20),
		Files: []metainfo.FileInfo{
			{Path: []string{"..", "..", "outside.txt"}, Length: 5},
		},
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := &metainfo.MetaInfo{InfoBytes: infoBytes}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatal(err)
	}

	if _, err := InspectTorrentBytes(buf.Bytes()); err == nil {
		t.Fatalf("期望拒绝包含 .. 的路径穿越种子")
	}
	if _, err := DownloadTorrent(context.Background(), "", buf.Bytes(), TorrentDownloadConfig{
		SaveDir:         t.TempDir(),
		NoDHT:           true,
		DisableTrackers: true,
	}); err == nil {
		t.Fatalf("期望 DownloadTorrent 拒绝包含 .. 的路径穿越种子")
	}
}

func TestTorrentInspectAndLoopbackSelectiveAndMagnetDownload(t *testing.T) {
	seedRoot := t.TempDir()
	outTorrentDir := t.TempDir()
	outMagnetDir := t.TempDir()

	data1 := make([]byte, 24*1024+137)
	data2 := make([]byte, 19*1024+511)
	data3 := make([]byte, 22*1024+99)
	if _, err := io.ReadFull(rand.Reader, data1); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(rand.Reader, data2); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(rand.Reader, data3); err != nil {
		t.Fatal(err)
	}

	files := map[string][]byte{
		"01_intro.mp4":    data1,
		"sub/02_main.mkv": data2,
		"03_bonus.bin":    data3,
	}
	torrentPath, mi := buildTestMultiFileTorrent(t, seedRoot, "testpack", files)
	_, seederAddr := startLocalTestSeeder(t, seedRoot, mi)

	// 1. 验证离线 InspectTorrent
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inspected, err := InspectTorrent(ctx, torrentPath, TorrentDownloadConfig{})
	if err != nil {
		t.Fatalf("InspectTorrent 失败: %v", err)
	}
	if inspected.Name != "testpack" || len(inspected.Files) != 3 {
		t.Fatalf("InspectTorrent 结果不符合预期: %+v", inspected)
	}

	// 找到 01_intro.mp4 和 sub/02_main.mkv 对应的 1-based 序号，排除 03_bonus.bin
	var wantedExprParts []string
	var excludedRelPath string
	for _, f := range inspected.Files {
		if strings.HasSuffix(f.Path, "03_bonus.bin") {
			excludedRelPath = f.Path
			continue
		}
		wantedExprParts = append(wantedExprParts, fmt.Sprintf("%d", f.Index))
	}

	// 2. 验证通过本地 .torrent 选择性下载（不下载 03_bonus.bin）
	selRes, err := DownloadTorrent(ctx, torrentPath, nil, TorrentDownloadConfig{
		SaveDir:         outTorrentDir,
		FilesExpr:       strings.Join(wantedExprParts, ","),
		NoDHT:           true,
		DisableTrackers: true,
		InitialPeers:    []string{seederAddr},
		MetadataTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("选择性 BT 下载失败: %v", err)
	}
	if len(selRes.Files) != 2 {
		t.Fatalf("期望下载 2 个文件，实际 %d 个", len(selRes.Files))
	}
	for _, df := range selRes.Files {
		gotBytes, err := os.ReadFile(df.LocalPath)
		if err != nil {
			t.Fatalf("读取落盘文件 %s 失败: %v", df.LocalPath, err)
		}
		var want []byte
		switch {
		case strings.HasSuffix(df.RelPath, "01_intro.mp4"):
			want = data1
		case strings.HasSuffix(df.RelPath, "02_main.mkv"):
			want = data2
		default:
			t.Fatalf("意外下载了未选中文件: %+v", df)
		}
		if !bytes.Equal(gotBytes, want) {
			t.Fatalf("文件 %s 内容校验不一致: got=%d want=%d", df.RelPath, len(gotBytes), len(want))
		}
	}

	excludedDisk := filepath.Join(outTorrentDir, "testpack", filepath.FromSlash(excludedRelPath))
	if _, err := os.Stat(excludedDisk); !os.IsNotExist(err) {
		t.Fatalf("未选中文件不应留在磁盘上: %s (err=%v)", excludedDisk, err)
	}
	if _, err := os.Stat(excludedDisk + ".part"); !os.IsNotExist(err) {
		t.Fatalf("未选中 .part 残片不应留在磁盘上: %s.part", excludedDisk)
	}

	// 3. 验证纯磁力链接拉取元数据并匹配下载单个文件
	magnetURI := "magnet:?xt=urn:btih:" + inspected.InfoHash
	magRes, err := DownloadTorrent(ctx, magnetURI, nil, TorrentDownloadConfig{
		SaveDir:         outMagnetDir,
		MatchExpr:       "*.mkv",
		NoDHT:           true,
		DisableTrackers: true,
		InitialPeers:    []string{seederAddr},
		MetadataTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("纯磁力链接下载失败: %v", err)
	}
	if len(magRes.Files) != 1 || !strings.HasSuffix(magRes.Files[0].RelPath, "02_main.mkv") {
		t.Fatalf("磁力匹配筛选结果不符合预期: %+v", magRes.Files)
	}
	gotMKV, err := os.ReadFile(magRes.Files[0].LocalPath)
	if err != nil {
		t.Fatalf("读取磁力下载文件失败: %v", err)
	}
	if !bytes.Equal(gotMKV, data2) {
		t.Fatalf("磁力下载文件内容不一致: got=%d want=%d", len(gotMKV), len(data2))
	}

	// 4. 验证无 Peer 磁力链接触发 MetadataTimeout 超时
	_, timeoutErr := InspectTorrent(ctx, "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TorrentDownloadConfig{
		NoDHT:           true,
		DisableTrackers: true,
		MetadataTimeout: 450 * time.Millisecond,
	})
	if timeoutErr == nil || !strings.Contains(timeoutErr.Error(), "超时") {
		t.Fatalf("期望无 Peer 磁力触发元数据超时错误，实际: %v", timeoutErr)
	}
}
