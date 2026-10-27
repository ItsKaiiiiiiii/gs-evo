// Package skillpkg 实现 skill 内容包的打包/解包、manifest 解析与 ed25519 签名验签。
// bundle 形态：目录 → tar.gz（内容寻址 sha256），见 DESIGN.md D5。
package skillpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PackDir 将目录打包为 tar.gz 写入 w，返回 (sha256hex, size)。
// 归档内为相对路径（不含父目录）；跳过符号链接与 .gs-evo-* 内部文件。
func PackDir(dir string, w io.Writer) (string, int64, error) {
	h := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(w, h))
	tw := tar.NewWriter(gz)

	var size int64
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil // 不跟随符号链接
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(filepath.Base(rel), ".gs-evo-") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		hdr, hdrErr := tar.FileInfoHeader(info, "")
		if hdrErr != nil {
			return hdrErr
		}
		hdr.Name = filepath.ToSlash(rel)
		if !info.Mode().IsRegular() {
			return nil // 只打包常规文件与目录
		}
		if hdrErr := tw.WriteHeader(hdr); hdrErr != nil {
			return hdrErr
		}
		if info.Mode().IsRegular() {
			f, fErr := os.Open(path)
			if fErr != nil {
				return fErr
			}
			n, cpErr := io.Copy(tw, f)
			size += n
			_ = f.Close()
			if cpErr != nil {
				return cpErr
			}
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	if err := tw.Close(); err != nil {
		return "", 0, err
	}
	if err := gz.Close(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// ExtractTarGz 将 r 解包到 dest（须为空目录或不存在）。
// 拒绝绝对路径与 .. 穿越条目。
func ExtractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("unsafe entry %q", hdr.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o755)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			_ = f.Close()
		default:
			// 其余类型（链接/设备等）一律跳过
		}
	}
}

// HashDir 计算目录内容哈希：对（相对路径, 文件内容）按序 sha256。
// 用于冲突检测（磁盘 vs state.json 记录）与 manifest.content_hash。
func HashDir(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(filepath.Base(rel), ".gs-evo-") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		_, _ = fmt.Fprintf(h, "f:%s\n", filepath.ToSlash(rel))
		f, fErr := os.Open(path)
		if fErr != nil {
			return fErr
		}
		_, cpErr := io.Copy(h, f)
		_ = f.Close()
		return cpErr
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// RenameWithRetry 原子 rename，Windows 上目标被占用时退避重试（§7.3）。
func RenameWithRetry(oldPath, newPath string, attempts int) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = os.Rename(oldPath, newPath); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 300 * time.Millisecond)
	}
	return err
}

// ExtractFile 从 tar.gz bundle 中提取单个文件内容（用于 diff/审阅，§7.5）。
func ExtractFile(bundlePath, name string) ([]byte, error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, os.ErrNotExist
		}
		if err != nil {
			return nil, err
		}
		if filepath.Clean(hdr.Name) == filepath.Clean(name) {
			return io.ReadAll(tr)
		}
	}
}

// RepackWithSkillMD 解开 src bundle，用 newSkillMD 替换其中的 entry 文件后重打包。
// 返回新 bundle 字节。用于进化候选生成（§7.5 ③）。
func RepackWithSkillMD(srcPath, entry, newSkillMD string) ([]byte, string, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, "", err
	}
	tr := tar.NewReader(gz)

	var out bytes.Buffer
	h := sha256.New()
	gzw := gzip.NewWriter(io.MultiWriter(&out, h))
	tw := tar.NewWriter(gzw)

	replaced := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, "", err
		}
		if filepath.Clean(hdr.Name) == filepath.Clean(entry) {
			content = []byte(newSkillMD)
			replaced = true
		}
		hdr.Size = int64(len(content))
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, "", err
		}
		if _, err := tw.Write(content); err != nil {
			return nil, "", err
		}
	}
	if !replaced {
		return nil, "", fmt.Errorf("entry %q not found in bundle", entry)
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	if err := gzw.Close(); err != nil {
		return nil, "", err
	}
	return out.Bytes(), hex.EncodeToString(h.Sum(nil)), nil
}

// PackSingle 将单文件打包为 tar.gz（client.update 发布包用）。
func PackSingle(name string, content []byte) ([]byte, error) {
	var out bytes.Buffer
	h := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(&out, h))
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg,
		Mode: 0o755, Size: int64(len(content)), ModTime: time.Now()}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	_ = h
	return out.Bytes(), nil
}

// SumBytes 内容 sha256（发布链路用）。
func SumBytes(b []byte) string { return hex.EncodeToString(sha256Hash(b)) }

func sha256Hash(b []byte) []byte {
	h := sha256.New()
	h.Write(b)
	return h.Sum(nil)
}
