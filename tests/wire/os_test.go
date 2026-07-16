package wire

// doStat wraps os.Stat so the wire test files don't import `os`
// directly. Keeps the per-file imports minimal.
import "os"

// statInfo is the minimal projection of os.FileInfo we care about.
type statInfo struct{ Size int64 }

// doStat is a thin wrapper that returns just the size (other fields
// are ignored to keep the wire package import surface minimal).
func doStat(path string) (statInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return statInfo{}, err
	}
	return statInfo{Size: info.Size()}, nil
}
