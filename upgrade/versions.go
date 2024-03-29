package upgrade

import (
	"fmt"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/pflag"
)

type Ref interface {
	fmt.Stringer
	refish()
}

type Version struct {
	SemVer *semver.Version
}

func (*Version) refish() {}

func (x *Version) String() string {
	return x.SemVer.Original()
}

type HashReference struct {
	GitHash string
}

func (*HashReference) refish() {}

func (x *HashReference) String() string {
	return x.GitHash
}

type Latest struct{}

func (*Latest) refish() {}

func (x *Latest) String() string {
	return "<latest>"
}

func ParseRef(s string) (Ref, error) {
	if s == "" {
		return nil, fmt.Errorf("empty string is not a valid version")
	}
	if s == "latest" {
		return &Latest{}, nil
	}
	v, err := semver.NewVersion(s)
	if err == nil {
		return &Version{v}, nil
	}
	// assume this is a hash reference otherwise
	return &HashReference{s}, nil
}

func RefFlag(r *Ref) pflag.Value { return &refFlag{r} }

type refFlag struct{ r *Ref }

func (f *refFlag) String() string {
	if f == nil || f.r == nil || *f.r == nil {
		return ""
	}
	return (*f.r).String()
}

func (f *refFlag) Set(s string) error {
	r, err := ParseRef(s)
	if err == nil {
		*f.r = r
	}
	return err
}

func (*refFlag) Type() string { return "ref" }
