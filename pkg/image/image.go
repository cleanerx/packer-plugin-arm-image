package image

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/hashicorp/packer/packer"
	filetype "gopkg.in/h2non/filetype.v1"
	"gopkg.in/h2non/filetype.v1/matchers"

	"github.com/ulikunitz/xz"
)

type nilUi struct{}

type imageOpener struct {
	ui packer.Ui
}

func (*nilUi) Ask(string) (string, error) {
	return "", errors.New("no ui available")
}
func (*nilUi) Say(string) {

}
func (*nilUi) Message(string) {

}
func (*nilUi) Error(string) {

}
func (*nilUi) Machine(string, ...string) {

}

func NewImageOpener(ui packer.Ui) ImageOpener {
	if ui == nil {
		ui = &nilUi{}
	}
	return &imageOpener{ui: ui}
}

type fileImage struct {
	io.ReadCloser
	size uint64
}

func (f *fileImage) SizeEstimate() uint64 { return f.size }
func openImage(file *os.File) (Image, error) {

	finfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	fsize := finfo.Size()

	ret := fileImage{ReadCloser: file, size: uint64(fsize)}
	return &ret, nil

}

func (s *imageOpener) Open(fpath string) (Image, error) {
	t, _ := filetype.MatchFile(fpath)

	f, err := os.Open(fpath)
	if err != nil {
		return nil, err
	}

	switch t {
	case matchers.TypeZip:
		s.ui.Say("Image is a zip file.")
		return s.openzip(f)
	case matchers.TypeXz:
		s.ui.Say("Image is a xz file.")
		return s.openxz(f)
	default:
		return openImage(f)
	}

}

func (s *imageOpener) openzip(f *os.File) (Image, error) {
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	fstat, err := f.Stat()
	if err != nil {
		return nil, err
	}

	r, err := zip.NewReader(f, fstat.Size())
	if err != nil {
		return nil, err
	}

	if len(r.File) != 1 {
		return nil, errors.New("support for only zip files with one file.")
	}

	zippedfile := r.File[0]
	s.ui.Say("Unzipping " + zippedfile.Name)
	zippedfileReader, err := zippedfile.Open()
	if err != nil {
		return nil, err
	}

	//transfer ownership
	mc := &multiCloser{zippedfileReader, []io.Closer{zippedfileReader, f}, zippedfile.UncompressedSize64}
	f = nil

	return mc, nil
}

func (s *imageOpener) xzFastlane(f *os.File) (Image, error) {

	xzcat := exec.Command("xzcat")

	// fast path, use xzcat
	xzcat.Stdin = f
	r, err := xzcat.StdoutPipe()

	if err != nil {
		return nil, err
	}
	if err := xzcat.Start(); err != nil {
		return nil, err
	}

	go func() {
		xzcat.Wait()
	}()

	// use mc for size estimate
	mc := &multiCloser{r, []io.Closer{r}, 0}

	return mc, nil

}

func (s *imageOpener) openxz(f *os.File) (Image, error) {
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	// check if available:
	if exec.Command("which", "xzcat").Run() == nil {
		ret, err := s.xzFastlane(f)
		if err == nil {
			f = nil
			return ret, err
		}
	}
	// slow lane here
	r, err := xz.NewReader(f)
	if err != nil {
		return nil, err
	}

	//transfer ownership
	mc := &multiCloser{r, []io.Closer{f}, 0}
	f = nil

	return mc, nil
}

type multiCloser struct {
	io.Reader
	c []io.Closer

	sizeEstimate uint64
}

func (n *multiCloser) Close() error {
	for _, c := range n.c {
		c.Close()
	}
	return nil
}

func (f *multiCloser) SizeEstimate() uint64 { return f.sizeEstimate }
