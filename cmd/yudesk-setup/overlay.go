package main

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	setupMagic      = "YUDESK_SETUP_V1\x00"
	setupFooterSize = 16 + 8 + sha256.Size
	maxPayloadSize  = 256 << 20
)

type payloadDescriptor struct {
	offset int64
	size   int64
	hash   [sha256.Size]byte
}

func readPayloadDescriptor(file *os.File) (payloadDescriptor, error) {
	info, err := file.Stat()
	if err != nil {
		return payloadDescriptor{}, err
	}
	if info.Size() <= setupFooterSize {
		return payloadDescriptor{}, errors.New("安装包中没有 YuDesk 程序")
	}
	footer := make([]byte, setupFooterSize)
	if _, err = file.ReadAt(footer, info.Size()-setupFooterSize); err != nil {
		return payloadDescriptor{}, err
	}
	if string(footer[:16]) != setupMagic {
		return payloadDescriptor{}, errors.New("安装包格式不正确")
	}
	size := int64(binary.LittleEndian.Uint64(footer[16:24]))
	if size <= 0 || size > maxPayloadSize || size > info.Size()-setupFooterSize {
		return payloadDescriptor{}, errors.New("安装包程序大小不正确")
	}
	descriptor := payloadDescriptor{offset: info.Size() - setupFooterSize - size, size: size}
	copy(descriptor.hash[:], footer[24:])
	return descriptor, nil
}

func verifyPayload(file *os.File, descriptor payloadDescriptor) error {
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(file, descriptor.offset, descriptor.size)); err != nil {
		return err
	}
	if got := hash.Sum(nil); !equalBytes(got, descriptor.hash[:]) {
		return errors.New("安装包校验失败，请从 YuDesk 官网重新下载")
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}

func extractPayload(packagePath, outputPath string) ([sha256.Size]byte, error) {
	packageFile, err := os.Open(packagePath)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer packageFile.Close()
	descriptor, err := readPayloadDescriptor(packageFile)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if err = verifyPayload(packageFile, descriptor); err != nil {
		return [sha256.Size]byte{}, err
	}
	if err = os.MkdirAll(filepath.Dir(outputPath), 0700); err != nil {
		return [sha256.Size]byte{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(outputPath), ".yudesk-payload-*.exe")
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	_, copyErr := io.Copy(temporary, io.NewSectionReader(packageFile, descriptor.offset, descriptor.size))
	closeErr := temporary.Close()
	if copyErr != nil {
		return [sha256.Size]byte{}, copyErr
	}
	if closeErr != nil {
		return [sha256.Size]byte{}, closeErr
	}
	if err = os.Chmod(temporaryPath, 0700); err != nil {
		return [sha256.Size]byte{}, err
	}
	if err = replaceFile(temporaryPath, outputPath); err != nil {
		return [sha256.Size]byte{}, err
	}
	return descriptor.hash, nil
}

func packageSetup(stubPath, payloadPath, outputPath string) error {
	for _, path := range []string{stubPath, payloadPath, outputPath} {
		if path == "" {
			return errors.New("打包路径不能为空")
		}
	}
	stubAbs, _ := filepath.Abs(stubPath)
	payloadAbs, _ := filepath.Abs(payloadPath)
	outputAbs, _ := filepath.Abs(outputPath)
	if outputAbs == stubAbs || outputAbs == payloadAbs || stubAbs == payloadAbs {
		return errors.New("安装器、程序和输出路径必须不同")
	}
	payload, err := os.Open(payloadPath)
	if err != nil {
		return err
	}
	defer payload.Close()
	info, err := payload.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxPayloadSize {
		return errors.New("YuDesk 程序文件不正确")
	}
	stub, err := os.Open(stubPath)
	if err != nil {
		return err
	}
	defer stub.Close()
	if err = os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(outputPath), ".yudesk-setup-*.exe")
	if err != nil {
		return err
	}
	temporaryPath := out.Name()
	defer os.Remove(temporaryPath)
	if _, err = io.Copy(out, stub); err != nil {
		out.Close()
		return err
	}
	hash := sha256.New()
	if _, err = io.Copy(io.MultiWriter(out, hash), payload); err != nil {
		out.Close()
		return err
	}
	footer := make([]byte, setupFooterSize)
	copy(footer[:16], setupMagic)
	binary.LittleEndian.PutUint64(footer[16:24], uint64(info.Size()))
	copy(footer[24:], hash.Sum(nil))
	if _, err = out.Write(footer); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = replaceFile(temporaryPath, outputPath); err != nil {
		return fmt.Errorf("写入安装包: %w", err)
	}
	return nil
}

func replaceFile(source, target string) error {
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
}
