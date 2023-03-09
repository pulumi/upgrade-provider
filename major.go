package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func major(name, newMajorVersion string) error {
	mu := majorUtil{}
	info := mu.currentProvider(name)
	if err := mu.fixupGoFiles(info, newMajorVersion); err != nil {
		return err
	}
	return nil
}

type majorProviderInfo struct {
	name   string
	prefix string
	major  string
}

type majorUtil struct{}

func (u *majorUtil) fixupGoFiles(prov majorProviderInfo, ver string) error {
	files, err := u.findGoFiles()
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := u.fixupGoFile(prov, ver, file); err != nil {
			return err
		}
	}
	return nil
}

func (u *majorUtil) fixupGoFile(prov majorProviderInfo, ver, file string) error {
	replace := fmt.Sprintf("%q -> %q",
		fmt.Sprintf("%s/%s", prov.prefix, prov.major),
		fmt.Sprintf("%s/%s", prov.prefix, ver))
	if err := u.gofmtr(replace, file); err != nil {
		return err
	}

	replaceVersionPkg := fmt.Sprintf("%q -> %q",
		fmt.Sprintf("%s/%s/pkg/version", prov.prefix, prov.major),
		fmt.Sprintf("%s/%s/pkg/version", prov.prefix, ver))
	if err := u.gofmtr(replaceVersionPkg, file); err != nil {
		return err
	}

	sdk := strings.TrimSuffix(prov.prefix, "/provider") + "/sdk"
	replaceGoSdkPkg := fmt.Sprintf("%q -> %q",
		fmt.Sprintf("%s/%s/go/%s", sdk, prov.major, prov.name),
		fmt.Sprintf("%s/%s/go/%s", sdk, ver, prov.name))
	if err := u.gofmtr(replaceGoSdkPkg, file); err != nil {
		return err
	}

	replaceSdkPkg := fmt.Sprintf("%q -> %q",
		fmt.Sprintf("%s/%s", sdk, prov.major),
		fmt.Sprintf("%s/%s", sdk, ver))
	if err := u.gofmtr(replaceSdkPkg, file); err != nil {
		return err
	}

	return nil
}

func (u *majorUtil) gofmtr(replace, file string) error {
	cmd := exec.Command("gofmt", "-r", replace, "-w", file)
	fmt.Printf("gofmt -r %s -w %s\n", replace, file)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (u *majorUtil) findGoFiles() ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z", "**/*.go")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	fs := strings.Split(stdout.String(), string(rune(0)))
	result := []string{}
	for _, f := range fs {
		if f == "" {
			continue
		}
		result = append(result, f)
	}
	return result, nil
}

func (u *majorUtil) currentProvider(name string) majorProviderInfo {
	gomod, _ := os.ReadFile("provider/go.mod")
	firstline := strings.Trim(strings.Split(string(gomod), "\n")[0], "\r\n")
	frags := strings.Split(firstline, "/")
	prefix := strings.TrimPrefix(strings.Join(frags[0:len(frags)-1], "/"), "module ")
	ver := frags[len(frags)-1]
	return majorProviderInfo{name, prefix, ver}
}
