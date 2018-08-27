package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/build"
	"golang.org/x/tools/imports"
	"golang.org/x/tools/refactor/rename"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	debug      = false
	implPrefix = "NoIFGo"
	helpUsage  = `NoIFGo is a go tool wrapper that optimizes source code by replacing interfaces with their implementations and then using the go tool on the resulting code.

Usage:

	noifgo	[args]	e.g. noifgo build -a -gcflags "-m -m"

The args are the same arguments the go tool expects, since this tool is a wrapper for it.
For help, use "noifgo help".

`
	helpNotFoundHiddenFile = `Could not find the hidden file .noifgo which should be placed in your projects root folder.

`
)

type reference struct {
	filepath string
	row      int
	col      int
}
type ifImplementation struct {
	filepath string
	name     string
	row      int
	col      int
}
type taggedInterface struct {
	filepath string
	name     string
	row      int
	col      int
}
type srcFileToBackup struct {
	filepath string
	backedUp bool
}

type srcFilesToBackup []srcFileToBackup

func (s *srcFilesToBackup) Add(filepath string) {
	if s == nil {
		sf := srcFileToBackup{filepath: filepath, backedUp: false}
		*s = append(*s, sf)
		return
	}
	for _, sf := range *s {
		if filepath == sf.filepath {
			return
		}
	}
	sf := srcFileToBackup{filepath: filepath, backedUp: false}
	*s = append(*s, sf)
}

func main() {
	if debug {
		fmt.Printf("main.main() called\n")
		defer fmt.Printf("main.main() returned\n")
	}
	var rootFolder string
	var tag = []byte("noifgo:ifdef")
	var hiddenFilename = ".noifgo"
	var srcFilesToBackup srcFilesToBackup
	var processedInterfaces []taggedInterface

	// Sets description for this tool
	flag.Usage = func() {
		fmt.Printf(helpUsage)
	}
	//var args = flag.String("args", "", "Enter go tool arguments, see \"go help build\" for help.")
	flag.Parse()
	args := flag.Args()

	if len(args) == 0 {
		fmt.Printf(helpUsage)
		return
	}

	// - Finds project rootFolder ---------------------------------------------------------
	wd, err := os.Getwd()
	if err != nil {
		fmt.Printf("could not get current working directory: %s\n", err)
	}
	if debug {
		fmt.Printf("current working directory: %s\n", wd)
	}
	var hiddenFilepath string
	hiddenFilepath = filepath.Join(wd, hiddenFilename)
	fi, _ := os.Open(hiddenFilepath)
	if fi != nil {
		rootFolder = wd
	}
	fi.Close()
	if rootFolder == "" {
		wdParts := strings.Split(wd, string(filepath.Separator))
		if wdParts == nil {
			fmt.Printf("Quitting due to working directory is nil\n")
			os.Exit(1)
		}
		for i := 0; i < len(wdParts); i++ {
			var k int
			for j := 0; j < i+1; j++ {
				k = k + len(wdParts[len(wdParts)-1-j])
			}
			k = k + i
			//fmt.Printf("k: %d, wd[:len(wd)-k]: %s\n", k, wd[:len(wd)-k])
			hiddenFilepath = filepath.Join(wd[:len(wd)-k], hiddenFilename)
			fi, _ := os.Open(hiddenFilepath)
			if fi != nil {
				rootFolder = wd[:len(wd)-k]
				fi.Close()
				break
			}
			fi.Close()
		}
	}
	if rootFolder == "" {
		fmt.Printf(helpNotFoundHiddenFile)
		return
	}
	if debug {
		fmt.Printf("rootFolder: %s\n", rootFolder)
	}

	// - Finds next tagged interface to process ---------------------------------------------
	for {
		if debug {
			fmt.Printf("Finds next tagged interface to process...\n")
		}
		taggedIf := nextInterfaceToProcess(rootFolder, &processedInterfaces, tag)
		// if no more interfaces to process
		if taggedIf == nil {
			break
		}
		srcFilesToBackup.Add(taggedIf.filepath)
		if debug {
			fmt.Printf("taggedIf: %v\n", taggedIf)
		}

		// - Finds implementations of tagged interface ---------------------------------------
		impl, err := implByIf(taggedIf.filepath, taggedIf.row, taggedIf.col)
		if err != nil {
			fmt.Printf("could not get implementation by interface: %s\n", err)
			break
		}
		if debug {
			fmt.Printf("impl: %v\n", impl)
		}
		srcFilesToBackup.Add(impl.filepath)

		// - Finds tagged interface implementation references and adds them to srcFilesToBackup -------
		implRefs, err := implRefs(impl.filepath, impl.row, impl.col)
		if err != nil {
			fmt.Printf("could not get implementation references by interface: %s\n", err)
			break
		}
		for _, implRef := range implRefs {
			if debug {
				fmt.Printf("implRef: %v\n", implRef)
			}
			srcFilesToBackup.Add(implRef.filepath)
		}
		//fmt.Printf("implRefs: %v\n", implRefs)

		// - Finds tagged interface references --------------------------------------------------
		ifRefs, err := ifRefs(taggedIf.filepath, taggedIf.row, taggedIf.col)
		if err != nil {
			fmt.Printf("could not get interface references by interface: %s\n", err)
			break
		}
		for _, ifRef := range ifRefs {
			if debug {
				fmt.Printf("ifRef: %v\n", ifRef)
			}
			srcFilesToBackup.Add(ifRef.filepath)
		}

		// Creates a backup for each source file to backup
		for i := 0; i < len(srcFilesToBackup); i++ {
			if srcFilesToBackup[i].backedUp {
				continue
			}
			if err = copyFile(srcFilesToBackup[i].filepath, srcFilesToBackup[i].filepath+".txt"); err != nil {
				fmt.Printf("could not copy file %s: %s\n", srcFilesToBackup[i].filepath, err)
				return
			}
			srcFilesToBackup[i].backedUp = true

		}

		// Adds a prefix to interface implementation that also exports it
		implPos, err := toPos(impl.filepath, impl.row, impl.col)
		if err != nil {
			fmt.Printf("could not get ifImplPos for %s on row %d and column %d\n", impl.filepath, impl.row, impl.col)
			return
		}
		if err = renameRefMany(fmt.Sprintf("%s:#%d", impl.filepath, implPos), implPrefix+impl.name); err != nil {
			fmt.Printf("could not rename implementation %s in file %s\n", impl.name, impl.filepath)
			return
		}

		// Renames interface references to the implementation
		var lastRefFilepath string
		var lastRefRow int
		var lastRowGrowth int
		for _, ifRef := range ifRefs {
			if lastRefFilepath == ifRef.filepath && lastRefRow == ifRef.row {
				ifRef.col = ifRef.col + lastRowGrowth
			}
			refPos, err := toPos(ifRef.filepath, ifRef.row, ifRef.col)
			if err != nil {
				fmt.Printf("could not get refPos for %s on row %d and column %d\n", ifRef.filepath, ifRef.row, ifRef.col)
				return
			}
			convertTo, err := shouldConvertTo(ifRef.filepath, ifRef.row, taggedIf.name)
			if err != nil {
				fmt.Printf("could not parse noifgo tag: %s\n", err)
				return
			}
			var typePrefix string
			if convertTo == "p" {
				typePrefix = "*"
			} else {
				typePrefix = ""
			}
			refAndIfInSamePkg := referencesInSamePkg(ifRef.filepath, taggedIf.filepath)
			refAndImplInSamePkg := referencesInSamePkg(ifRef.filepath, impl.filepath)
			var pkgPrefix string
			if refAndImplInSamePkg {
				pkgPrefix = ""
			} else {
				pkgPrefix = pkgFromFilepath(impl.filepath) + "."
			}
			if err = renameRefSingle(ifRef.filepath, taggedIf.name, typePrefix+pkgPrefix+implPrefix+impl.name, refPos, refAndIfInSamePkg, pkgFromFilepath(taggedIf.filepath)); err != nil {
				return
			}
			lastRefFilepath = ifRef.filepath
			lastRefRow = ifRef.row
			lastRowGrowth = len(typePrefix) + 9
		}
		for _, ifRef := range ifRefs {
			// Run GoImports on all files where the interface references were renamed to the implementation
			if err = fixImports(ifRef.filepath); err != nil {
				return
			}
		}
	}
	// Compiles project
	//argsParts := splitArgs(*args)
	runGoBuildCmd := exec.Command("go", args...)
	runGoBuildOutput, err := runGoBuildCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Failed: %s\n\n", err)
	} else {
		fmt.Printf("Successfully optimized and compiled project\n")
		fmt.Printf("%s\n\n", runGoBuildOutput)
	}
	// Restores initial state
	for _, backedUpFile := range srcFilesToBackup {
		if _, err := os.Stat(backedUpFile.filepath + ".txt"); os.IsNotExist(err) {
			// path/to/whatever does not exist
			fmt.Printf("backed up file %s not found\n", backedUpFile.filepath)
			continue
		}
		if err = os.Remove(backedUpFile.filepath); err != nil {
			fmt.Printf("could not remove backed up file %s: %s\n", backedUpFile.filepath, err)
			return
		}
		if err = os.Rename(backedUpFile.filepath+".txt", backedUpFile.filepath); err != nil {
			fmt.Printf("could not rename backed up file %s from .txt to .go: %s\n", backedUpFile.filepath, err)
			return
		}
	}
}

// splitArgs parses args and splits it by the space character. It does however allow spaces in double quoted text.
func splitArgs(args string) (argsSlice []string) {
	// example
	// build -a -gcflags "-m -m"
	// a b
	var splitPositions []int
	doubleQuotesFound := false
	ignoreSpace := false
	for k, v := range args {
		if v == '"' {
			if !doubleQuotesFound {
				doubleQuotesFound = true
				continue
			}
			doubleQuotesFound = false
			continue
		}
		if !doubleQuotesFound && !ignoreSpace && v == ' ' {
			splitPositions = append(splitPositions, k)
			ignoreSpace = true
			continue
		}
		if ignoreSpace && v != ' ' {
			ignoreSpace = false
		}
	}
	prevPos := 0
	for _, v := range splitPositions {
		argsSlice = append(argsSlice, args[prevPos:v])
		prevPos = v + 1
	}
	if prevPos >= len(args) {
		// cleans away all double quotes
		for k, v := range argsSlice {
			argsSlice[k] = strings.Replace(v, "\"", "", -1)
		}
		return
	}
	argsSlice = append(argsSlice, args[prevPos:])
	// cleans away all double quotes
	for k, v := range argsSlice {
		argsSlice[k] = strings.Replace(v, "\"", "", -1)
	}
	return
}

// shouldConvertTo scans the filepath looking for a special NoIFGo comment on the line before row.
// The comment should be of the form: //noifgo:{InterfaceName, ptr or value}. Given it finds
// such a special comment it returns either "p" for pointer or "v" for value and a nil error.
// If however something errors during the function call an empty string is returned and the error.
func shouldConvertTo(filepath string, row int, ifName string) (string, error) {
	//fmt.Printf("shouldConvertTo called with filepath %s, row: %d, ifName: %s\n", filepath, row, ifName)
	//defer fmt.Printf("shouldConvertTo returned\n")
	b, err := ioutil.ReadFile(filepath)
	if err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	curRow := 1
	var prevLine []byte
	for scanner.Scan() {
		if curRow != row {
			curRow++
			prevLine = scanner.Bytes()
			continue
		}
		break
	}
	prevLineParts := bytes.Split(prevLine, []byte("noifgo:"))
	//fmt.Printf("prevLineParts: %v\n", prevLineParts)
	if len(prevLineParts) != 2 {
		return "", fmt.Errorf("could not split line containing noifgo tag in two parts: %s", err)
	}
	if prevLineParts[1][0] != '{' {
		return "", errors.New("noifgo tag malformed: 'noifgo:' should be followed by a '{'")
	}
	closingCurlyBrIx := bytes.LastIndex(prevLineParts[1], []byte("}"))
	if closingCurlyBrIx == -1 {
		return "", errors.New("noifgo tag malformed: could not find closing '}'")
	}
	//fmt.Printf("prevLineParts[1][1:closingCurlyBrIx]: %s\n", prevLineParts[1][1:closingCurlyBrIx])
	keyValuePairs := bytes.Split(prevLineParts[1][1:closingCurlyBrIx], []byte(";"))
	//fmt.Printf("keyValuePairs: %s\n", keyValuePairs)
	//fmt.Printf("for each key value pair...\n")
	for _, kv := range keyValuePairs {
		keyValuePair := bytes.Split(bytes.TrimSpace(kv), []byte(","))
		//fmt.Printf("keyValuePair: %s\n", keyValuePair)
		if len(keyValuePair) != 2 {
			return "", errors.New("noifgo tag malfored: could not find key value pair, missing ','")
		}
		if bytes.Equal(keyValuePair[0], []byte(ifName)) {
			if bytes.Equal(keyValuePair[1], []byte("p")) {
				return "p", nil
			}
			if bytes.Equal(keyValuePair[1], []byte("v")) {
				return "v", nil
			}
			return "", errors.New("noifgo tag malformed: value in key value pair must either be 'p' or 'v'")
		}
	}
	return "", fmt.Errorf("noifgo tag malformed: could not find interface %s", ifName)
}

// referencesInSamePkg compares filepathA with filepathB and returns whether the two files
// belong to the same package.
func referencesInSamePkg(filepathA, filepathB string) bool {
	pathA := filepath.Dir(filepathA)
	pathB := filepath.Dir(filepathB)
	return pathA == pathB
}

// pkgFromFilepath returns the package name from the filepath fp.
func pkgFromFilepath(fp string) string {
	path := filepath.Dir(fp)
	return filepath.Base(path)
}

// toPos converts the row and col position in filepath to a byte array position used by guru.
func toPos(filepath string, row, col int) (int, error) {
	b, err := ioutil.ReadFile(filepath)
	if err != nil {
		return 0, err
	}
	var fileScannerLastAdvance int
	var pos int
	fileScanner := bufio.NewScanner(bytes.NewReader(b))
	fileScannerSplitFunc := func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		advance, token, err = bufio.ScanLines(data, atEOF)
		fileScannerLastAdvance = advance
		pos += advance
		return
	}
	// Set the split function for the scanning operation.
	fileScanner.Split(fileScannerSplitFunc)
	curRow := 1
	for fileScanner.Scan() {
		if curRow != row {
			curRow++
			continue
		}
		pos -= fileScannerLastAdvance
		pos += col
		break
	}
	// return pos - 1 since col's index starts with 1 instead of 0
	return pos - 1, nil
}

// copyFile copies the src file to dst. Any existing file will be overwritten and will not
// copy file attributes.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(0666))
	if err != nil {
		fmt.Printf("could not open file: %s\n", err)
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		fmt.Printf("could not copy file: %s\n", err)
		return err
	}
	return out.Close()
}

// renameRefMany renames a reference in one or more files.
func renameRefMany(filepos, to string) error {
	if debug {
		fmt.Printf("main.renameRefMany called: filepos: %s, to: %s\n", filepos, to)
		defer fmt.Printf("main.renameRefMany returned\n")
	}
	return rename.Main(&build.Default, filepos, "", to)
}

// renameRefSingle renames a single word in a single file.
func renameRefSingle(filepath, from, to string, pos int, refAndIfInSamePkg bool, ifPkgName string) error {
	if debug {
		fmt.Printf("main.renameRefSingle called: filepath: %s, from: %s, to: %s, pos: %d, refAndIfInSamePkg: %s, ifPkgName: %s\n", filepath, from, to, pos, refAndIfInSamePkg, ifPkgName)
		defer fmt.Printf("main.renameRefSingle returned\n")
	}
	b, err := ioutil.ReadFile(filepath)
	if err != nil {
		fmt.Printf("could not read file %s\n", err)
		return err
	}
	if !refAndIfInSamePkg {
		// from interactor.Interactor
		//                 ^ pos
		// to   interactor.Interactor
		//      ^ pos
		pos = pos - 1 - len(ifPkgName)
		// from Interactor
		// to   interactor.Interactor
		from = ifPkgName + "." + from
	}
	//fmt.Printf("b[:pos]: %s\n", string(b[:pos+1]))
	sizeChg := len(to) - len(from)
	newb := make([]byte, len(b)+sizeChg, len(b)+sizeChg)
	for k, v := range b {
		if k == pos {
			break
		}
		newb[k] = v
	}
	//fmt.Printf("newb up until from word: %s\n\n\n\n\n", string(newb))
	toBytes := []byte(to)
	for k, v := range toBytes {
		newb[pos+k] = v
	}
	//fmt.Printf("newb after adding to word: %s\n", string(newb))
	sAfterWord := b[pos+len(from):]
	for i := 0; i < len(sAfterWord); i++ {
		newb[pos+len(to)+i] = sAfterWord[i]
	}
	//fmt.Printf("%s\n", string(newb))
	in, err := os.Open(filepath)
	if err != nil {
		return err
	}
	defer in.Close()

	// wraps newb in a reader
	src := bytes.NewReader(newb)

	// writes the content of newb to the file given by filepath
	dst, err := os.OpenFile(filepath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(0666))
	if err != nil {
		fmt.Printf("could not open file: %s\n", err)
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	if err != nil {
		fmt.Printf("could not copy file: %s\n", err)
		return err
	}
	return nil
}

// implByIf uses guru to find the interface implementation for an interface given by the filepath, row and col arguments.
// If more than one implementation is encountered it returns an error and a nil ifImplementation.
func implByIf(fp string, row, col int) (*ifImplementation, error) {
	if debug {
		fmt.Printf("main.implByIf called: fp: %s, row: %d, col %d\n", fp, row, col)
		defer fmt.Printf("main.implByIf returned\n")
	}
	interfacePos, err := toPos(fp, row, col)
	if err != nil {
		return nil, err
	}
	if debug {
		fmt.Printf("interfacePos: %d\n", interfacePos)
	}
	findIfImplCmd := exec.Command("guru", "implements", fmt.Sprintf("%s:#%d", fp, interfacePos))

	// findIfImplCmdOutput example
	// ===========================
	// findIfImplCmd output: /Users/tobias/Cloud Storage/Sync/Tobias.Strandberg/Projects/go/src/private/p/expedition/noifgo/cmd/noifgo/if.go:4.6-4.10: interface type Adder
	// /Users/tobias/Cloud Storage/Sync/Tobias.Strandberg/Projects/go/src/private/p/expedition/noifgo/cmd/noifgo/if.go:14.6-14.8:        is implemented by struct type Lol
	// /Users/tobias/Cloud Storage/Sync/Tobias.Strandberg/Projects/go/src/private/p/expedition/noifgo/cmd/noifgo/if.go:8.6-8.10:         is implemented by struct type private/p/expedition/noifgo/example/lib/interaction.interactor
	findIfImplCmdOutput, err := findIfImplCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("could not run findIfImplCmd: %s\n", err)
		return nil, err
	}
	//fmt.Printf("findIfImplCmd output: %s\n", findIfImplCmdOutput)
	ifImplBytesScanner := bufio.NewScanner(bytes.NewReader(findIfImplCmdOutput))
	// skips the first line
	tooManyIfImpls := false
	var impl ifImplementation
	found := false
	ifImplBytesScanner.Scan()
	for ifImplBytesScanner.Scan() {
		if bytes.Contains(ifImplBytesScanner.Bytes(), []byte("_test.go")) {
			continue
		}
		if found {
			tooManyIfImpls = true
			break
		}
		// [...noifgo/if.go 14.5-14.8       "is implemented by struct type Lol"]
		ifImplBytesParts := bytes.Split(ifImplBytesScanner.Bytes(), []byte(":"))
		if len(ifImplBytesParts) != 3 {
			fmt.Printf("ifImplBytesParts does not contain three parts\n")
			continue
		}
		impl = ifImplementation{
			filepath: string(ifImplBytesParts[0]),
		}
		// [14.5 14.8]
		rowColDashParts := bytes.Split(ifImplBytesParts[1], []byte("-"))
		if len(rowColDashParts) != 2 {
			fmt.Printf("length of rowColDashParts not equal to 2\n")
			continue
		}
		// [14 5]
		rowColParts := bytes.Split(rowColDashParts[0], []byte("."))
		if len(rowColParts) != 2 {
			fmt.Printf("length of rowColParts not equal to 2\n")
			continue
		}
		// row is 14
		impl.row, err = strconv.Atoi(string(rowColParts[0]))
		if err != nil {
			fmt.Printf("could not parse row int from %s\n", rowColParts[0])
			continue
		}
		// col is 5
		impl.col, err = strconv.Atoi(string(rowColParts[1]))
		if err != nil {
			fmt.Printf("could not parse column int from %s\n", rowColParts[1])
			continue
		}
		// is implemented by struct type Lol
		//                              ^ ifImplPos
		ifImplPos := bytes.LastIndex(ifImplBytesParts[2], []byte(" "))
		if ifImplPos == -1 {
			fmt.Printf("could not find implementation name' '\n")
			continue
		}
		// from '/a/b/pkg.object' to 'pkg.object'
		dirtyImplName := filepath.Base(string(ifImplBytesParts[2][ifImplPos+1:]))
		// from 'pkg.object' to 'object'
		dirtyImplNameParts := strings.Split(dirtyImplName, ".")
		if len(dirtyImplNameParts) == 1 {
			impl.name = dirtyImplNameParts[0]
		} else {
			impl.name = dirtyImplNameParts[1]
		}
		found = true
		// adds implementation file to srcFilesToBackup
		//srcFilesToBackup.Add(impl.filepath)
		//fmt.Printf("ifImplName: %s\n", ifImplBytesParts[2][ifImplPos+1:])
	}
	if ifImplBytesScanner.Err() != nil {
		fmt.Printf("could not scan interface implementation bytes: %s\n", ifImplBytesScanner.Err())
		return nil, ifImplBytesScanner.Err()
	}
	if tooManyIfImpls {
		return nil, fmt.Errorf("Too many interface implementations")
	}

	return &impl, nil
}

// implRefs uses guru to find references to interface implementations in the file given by filepath.
// It returns a nil slice and an error if an error occurs.
func implRefs(filepath string, row, col int) ([]ifImplementation, error) {
	if debug {
		fmt.Printf("main.implRefs called: filepath: %s, row: %d, col %d\n", filepath, row, col)
		defer fmt.Printf("main.implRefs returned\n")
	}
	ifImplRefPos, err := toPos(filepath, row, col)
	if err != nil {
		fmt.Printf("could not get position from %s:%d.%d reference\n", filepath, row, col)
		return nil, fmt.Errorf("could not get position from %s:%d.%d reference", filepath, row, col)
	}
	if debug {
		fmt.Printf("ifImplRefPos: %d\n", ifImplRefPos)
	}
	findIfImplRefsCmd := exec.Command(
		"guru",
		"referrers",
		fmt.Sprintf(
			"%s:#%d",
			filepath,
			ifImplRefPos,
		),
	)
	findIfImplRefsCmdOutput, err := findIfImplRefsCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("could not run findIfImplRefsCmd: %s\n", err)
	}
	//fmt.Printf("findIfImplRefsCmdOutput: %s\n", findIfImplRefsCmdOutput)
	var impls []ifImplementation
	ifImplRefsBytesScanner := bufio.NewScanner(bytes.NewReader(findIfImplRefsCmdOutput))
	// skips the first line
	ifImplRefsBytesScanner.Scan()
	for ifImplRefsBytesScanner.Scan() {
		if bytes.Contains(ifImplRefsBytesScanner.Bytes(), []byte("_test.go")) {
			continue
		}
		ifImplRefsBytesParts := bytes.Split(ifImplRefsBytesScanner.Bytes(), []byte(":"))
		if len(ifImplRefsBytesParts) != 3 {
			fmt.Printf("ifImplRefsBytesParts does not contain three parts\n")
			continue
		}
		impl := ifImplementation{
			filepath: string(ifImplRefsBytesParts[0]),
		}
		rowColDashParts := bytes.Split(ifImplRefsBytesParts[1], []byte("-"))
		if len(rowColDashParts) != 2 {
			fmt.Printf("length of rowColDashParts not equal to 2\n")
			continue
		}
		rowColParts := bytes.Split(rowColDashParts[0], []byte("."))
		if len(rowColParts) != 2 {
			fmt.Printf("length of rowColParts not equal to 2\n")
			continue
		}
		rowColEndParts := bytes.Split(rowColDashParts[1], []byte("."))
		if len(rowColEndParts) != 2 {
			fmt.Printf("length of rowColEndParts not equal to 2\n")
			continue
		}
		impl.row, err = strconv.Atoi(string(rowColParts[0]))
		if err != nil {
			fmt.Printf("could not parse row int from %s\n", rowColParts[0])
			continue
		}
		impl.col, err = strconv.Atoi(string(rowColParts[1]))
		if err != nil {
			fmt.Printf("could not parse column int from %s\n", rowColParts[1])
			continue
		}
		endCol, err := strconv.Atoi(string(rowColEndParts[1]))
		if err != nil {
			fmt.Printf("could not parse end column int from %s\n", rowColEndParts[1])
			continue
		}
		impl.name = string(ifImplRefsBytesParts[2][impl.col : endCol+1])
		//fmt.Printf("impl.name: %s\n", impl.name)
		impls = append(impls, impl)
	}
	if ifImplRefsBytesScanner.Err() != nil {
		fmt.Printf("could not scan interface implementation references bytes: %s\n", ifImplRefsBytesScanner.Err())
		return nil, ifImplRefsBytesScanner.Err()
	}
	return impls, nil
}

// ifRefs uses guru to find interface references. Filepath is the file the interface definition resides in
// and row and col specifies the position in that file where the definition is located. If an error occurs
// a nil slice and an error are returned.
func ifRefs(filepath string, row, col int) ([]reference, error) {
	if debug {
		fmt.Printf("main.ifRefs called: filepath: %s, row: %d, col %d\n", filepath, row, col)
		defer fmt.Printf("main.ifRefs returned\n")
	}
	interfacePos, err := toPos(filepath, row, col)
	if err != nil {
		fmt.Printf("could not get position from %s:%d.%d reference\n", filepath, row, col)
		return nil, fmt.Errorf("could not get position from %s:%d.%d reference", filepath, row, col)
	}
	if debug {
		fmt.Printf("interfacePos: %d\n", interfacePos)
	}
	findIfRefsCmd := exec.Command("guru", "referrers", fmt.Sprintf("%s:#%d", filepath, interfacePos))

	// findIfRefsCmdOutput example
	// ===========================
	// findIfRefsCmdOutput: /Users/tobias/Cloud Storage/Sync/Tobias.Strandberg/Projects/go/src/private/p/expedition/noifgo/cmd/noifgo/if.go:4.6-4.10: references to type Adder interface{Add(a int, b int) int}
	// /Users/tobias/Cloud Storage/Sync/Tobias.Strandberg/Projects/go/src/private/p/expedition/noifgo/cmd/noifgo/main.go:236.8-236.12:   adder Adder
	findIfRefsCmdOutput, err := findIfRefsCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("could not run findIfRefsCmd: %s\n", err)
	}
	//fmt.Printf("findIfRefsCmdOutput: %s\n", findIfRefsCmdOutput)
	var refs []reference
	ifRefsBytesScanner := bufio.NewScanner(bytes.NewReader(findIfRefsCmdOutput))
	// skips the first line
	ifRefsBytesScanner.Scan()
	for ifRefsBytesScanner.Scan() {
		if bytes.Contains(ifRefsBytesScanner.Bytes(), []byte("_test.go")) {
			continue
		}
		ifRefsBytesParts := bytes.Split(ifRefsBytesScanner.Bytes(), []byte(":"))
		if len(ifRefsBytesParts) != 3 {
			fmt.Printf("ifRefsBytesParts does not contain three parts\n")
			continue
		}
		ref := reference{}
		ref.filepath = string(ifRefsBytesParts[0])
		rowColDashParts := bytes.Split(ifRefsBytesParts[1], []byte("-"))
		if len(rowColDashParts) != 2 {
			fmt.Printf("length of rowColDashParts not equal to 2\n")
			continue
		}
		rowColParts := bytes.Split(rowColDashParts[0], []byte("."))
		if len(rowColParts) != 2 {
			fmt.Printf("length of rowColParts not equal to 2\n")
			continue
		}
		ref.row, err = strconv.Atoi(string(rowColParts[0]))
		if err != nil {
			fmt.Printf("could not parse row int from %s\n", rowColParts[0])
			continue
		}
		ref.col, err = strconv.Atoi(string(rowColParts[1]))
		if err != nil {
			fmt.Printf("could not parse column int from %s\n", rowColParts[1])
			continue
		}
		refs = append(refs, ref)
	}
	if ifRefsBytesScanner.Err() != nil {
		fmt.Printf("could not scan interface references bytes: %s\n", ifRefsBytesScanner.Err())
		return nil, ifRefsBytesScanner.Err()
	}

	return refs, nil
}

// nextInterfaceToProcess traverses the file and folder structure recursively starting in rootFolder looking for
// tagged interfaces. Each tagged interface it encounters it stores in processedInterfaces to prevent it from
// returning the same interface twice. When there are no more tagged interfaces to return it returns nil.
func nextInterfaceToProcess(rootFolder string, processedInterfaces *[]taggedInterface, tag []byte) *taggedInterface {
	var taggedIf *taggedInterface
	filepath.Walk(rootFolder, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(info.Name()) != ".go" {
			return nil
		}
		b, err := ioutil.ReadFile(path)
		if err != nil {
			fmt.Printf("could not read file: %s\n", path)
		}
		var fileSlicePos int
		//var srcFileScannerLastAdvance int
		found := false
		var sb []byte
		srcFileScanner := bufio.NewScanner(bytes.NewReader(b))
		srcFileScannerSplitFunc := func(data []byte, atEOF bool) (advance int, token []byte, err error) {
			advance, token, err = bufio.ScanLines(data, atEOF)
			//srcFileScannerLastAdvance = advance
			fileSlicePos += advance
			return
		}
		row := 0
		// Sets the split function for the scanning operation.
		srcFileScanner.Split(srcFileScannerSplitFunc)
		for srcFileScanner.Scan() {
			row++
			//fmt.Printf("fileSlicePos: %d\n", fileSlicePos)
			sb = srcFileScanner.Bytes()
			if bytes.Contains(sb, tag) {
				found = true
				continue
			}
			if !found {
				continue
			}
			found = false
			if len(sb) < 5 {
				continue
			}
			if sb[0] != 't' || sb[1] != 'y' || sb[2] != 'p' || sb[3] != 'e' || sb[4] != ' ' {
				continue
			}
			ifNameStartIx := 0
			ifNameEndIx := 0
			for i := 5; i < len(sb); i++ {
				if sb[i] == ' ' {
					if ifNameStartIx != 0 {
						ifNameEndIx = i - 1
						break
					}
					continue
				}
				if ifNameStartIx == 0 {
					ifNameStartIx = i
				}
			}
			if ifNameStartIx == 0 || ifNameEndIx == 0 {
				continue
			}
			if !bytes.Contains(sb[ifNameEndIx+1:], []byte("interface")) {
				continue
			}
			// line containing a tagged interface declaration found
			var interfaceName = string(sb[ifNameStartIx : ifNameEndIx+1])
			ifProcessed := false
			for _, processedIf := range *processedInterfaces {
				if interfaceName == processedIf.name && path == processedIf.filepath {
					//fmt.Printf("interfaceName equals processedIf.name which is %s\n", interfaceName)
					ifProcessed = true
					break
				}
			}
			if ifProcessed {
				continue
			}
			taggedIf = &taggedInterface{
				name:     interfaceName,
				filepath: path,
				row:      row,
				col:      6,
			}
			// fmt.Printf("taggedIf.name: %s\n", taggedIf.name)
			*processedInterfaces = append(*processedInterfaces, *taggedIf)
			return nil
		}
		return nil
	})
	return taggedIf
}

// fixImports cleans up import statements in the file given by filepath.
func fixImports(filepath string) error {
	b, err := imports.Process(filepath, nil, nil)
	if err != nil {
		fmt.Printf("could not fix imports for file %s: %s\n", filepath, err)
		return err
	}
	src := bytes.NewReader(b)
	// writes the content of b to the file given by filepath
	dst, err := os.OpenFile(filepath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(0666))
	if err != nil {
		fmt.Printf("could not open file: %s\n", err)
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	if err != nil {
		fmt.Printf("could not copy file: %s\n", err)
		return err
	}
	return nil
}
