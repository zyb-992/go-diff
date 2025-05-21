package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/tools/cover"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	branch   = flag.String("branch", "", "the compared branch name")
	coverage = flag.String("file", "coverage.out", "the coverage file")
	mod      = modname()
	regex    = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
)

func init() {
	if flag.Parse(); !flag.Parsed() {
		log.Fatalf("parse flags failed")
	}
	if mod == "" {
		log.Fatalf("can't find the go.mod file")
	}
}

type Diff map[string][]lineInfo

type lineInfo struct {
	start, end int64
}

func parseDiff() Diff {
	// --diff-filter=ACMR 大写，仅关注新增、副本、修改、重命名的文件，隐式排除删除的文件
	cmd := exec.Command("git", []string{"diff", *branch, "HEAD", "--unified=0", "--diff-filter=ACMR"}...)

	diff, err := cmd.Output()
	if err != nil {
		log.Fatalf("exec git diff cmd error:%v", err)
	}

	diffs := make(Diff)
	scanner := bufio.NewScanner(bytes.NewReader(diff))
	var (
		newp     string
		trueFile bool
	)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "diff --git") {
			// 获取路径
			arr := strings.Split(line, " ")
			if len(arr) > 0 {
				_path := arr[len(arr)-1]
				if filepath.Ext(_path) != ".go" || strings.HasSuffix(_path, "_test.go") {
					trueFile = false
					continue
				}
				// trim prefix b/
				if _, ok := diffs[_path]; !ok {
					path := filepath.Join(mod, _path[2:])
					path = strings.ReplaceAll(path, "\\", "/")
					diffs[path] = make([]lineInfo, 0)
					newp = path
					trueFile = true
				}
			}
		} else if strings.HasPrefix(line, "@@") {
			if !trueFile {
				continue
			}

			sub := regex.FindStringSubmatch(line)
			// 0, 1, 2: 完整块 (1) (2)
			if len(sub) == 3 {
				start, _ := strconv.Atoi(sub[1])
				end, _ := strconv.Atoi(sub[2])
				diffs[newp] = append(diffs[newp], lineInfo{
					start: int64(start),
					end:   int64(end),
				})
			} else if len(sub) == 2 {
				// 可能只新增/修改了一行中的内容, git diff 中为+() @@ 而不是+(),1 @@
				start, _ := strconv.Atoi(sub[1])
				diffs[newp] = append(diffs[newp], lineInfo{
					start: int64(start),
					end:   0,
				})
			}
		}
	}

	return diffs
}

func parse(diffs Diff) {
	profiles, err := cover.ParseProfiles(*coverage)
	if err != nil {
		log.Fatalf("parse coverage file err:%v", err)
		return
	}

	buffer := bytes.NewBuffer(make([]byte, 0, 1024))
	buffer.WriteString("mode: count\n")

	// lines: 起始行
	fname2index := make(map[string]map[int]struct{})
	for fpath, lines := range diffs {
		for _, profile := range profiles {
			if profile.FileName != fpath {
				continue
			}

			if _, ok := fname2index[profile.FileName]; !ok {
				fname2index[profile.FileName] = make(map[int]struct{})
			}

			for _, line := range lines {
				for i, block := range profile.Blocks {
					_, ok := fname2index[profile.FileName][i]
					if ok {
						continue
					}
					bstart := int64(block.StartLine)
					bend := int64(block.EndLine)
					// 这里如何做条件判断???
					if (line.start >= bstart || line.start < bstart) && bend <= line.end+line.start {
						fname2index[profile.FileName][i] = struct{}{}
						buffer.WriteString(fmt.Sprintf("%s:%d.%d,%d.%d %d %d\n",
							profile.FileName,
							block.StartLine, block.StartCol,
							block.EndLine, block.EndCol,
							block.NumStmt, block.Count))
					}
				}
			}
		}
	}

	newf, _ := os.OpenFile("increment_coverage.out", os.O_CREATE|os.O_RDWR, 0666)
	newf.WriteString(buffer.String())
	defer newf.Close()

	return
}

func modname() string {
	f, err := os.Open("go.mod")
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	return strings.TrimPrefix(scanner.Text(), "module ")
}
