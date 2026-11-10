package main

// 秘密の名前と、Bitwarden Secrets Manager の ID の対応表。
// bws secret get は ID でしか引けない。名前から引くために secret list を呼ぶと、
// ほかの秘密の値まで全部門番に届くので、対応表を持つ。
// 4段目（許可リスト）で、この表に「どのコマンドに渡してよいか」を足す。
//
// 形：1行に「名前 ID」。# から後と空行は無視する。

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var (
	nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func loadSecrets(path string) (map[string]string, error) {
	real, err := trustedPath(path)
	if err != nil {
		return nil, fmt.Errorf("秘密の対応表: %w", err)
	}
	st, err := os.Lstat(real)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("秘密の対応表が普通のファイルではない: %s", real)
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseSecrets(f)
}

func parseSecrets(f io.Reader) (map[string]string, error) {
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("秘密の対応表 %d 行目：「名前 ID」の形ではない", n)
		}
		name, id := fields[0], strings.ToLower(fields[1])
		if !nameRe.MatchString(name) {
			return nil, fmt.Errorf("秘密の対応表 %d 行目：名前に使えない文字がある", n)
		}
		if !uuidRe.MatchString(id) {
			return nil, fmt.Errorf("秘密の対応表 %d 行目：ID が UUID の形ではない", n)
		}
		if _, dup := m[name]; dup {
			return nil, fmt.Errorf("秘密の対応表 %d 行目：名前 %s が2回出てくる", n, name)
		}
		m[name] = id
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("秘密の対応表が空")
	}
	return m, nil
}
