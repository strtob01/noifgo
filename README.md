# NoIFGo
Go tool wrapper that optimizes source code by replacing interfaces with their implementations before running the go tool on the resulting code.
Its purpose is to generate more performant binaries.

## Getting Started
Install or update gotags using the
`go get` command:

	go get -u github.com/strtob01/noifgo 

### Prerequisites

The official [Go Tools] need to be installed. NoIFGo uses the 'guru' tool which is a part of the [Go Tools].

### Setup

Follow the below steps to setup NoIFGo.
1. Download NoIFGo by running:
```
go get -u github.com/strtob01/noifgo
```
2. Go to the noifgo project folder and compile and install it by running:
```
go install
```
3. Make sure 'noifgo' is runnable from any folder. If not, add the folder where the binary resides to your PATH environment variable.

### Usage

To optimize the resulting binary for project *Foo*, go to its folder which would be %GOPATH%/src/foo and create an empty file called ".noifgo".
Edit the *Foo* project go source files with your favourite text editor and find the interface definitions that you would like to replace with their implementations.
Let's assume the *Foo* project contains the below interface definition:
```go
type Singer interface {
  Sing() error
}
```
To tag the *Singer* interface for *NoIFGo* enter the following comment on the line above the *Singer* definition as below:
```go
//noifgo:ifdef
type Singer interface {
  Sing() error
}
```
Next we need to tag every *Singer* reference we would like to replace with its implementation.
Let's assume there are two references in the file bar.go.
```go
type Idol struct {
  singerPtr   Singer
  singerValue Singer
}
```
When tagging references we also need to clarify for *NoIFGo* whether we want the reference to be replaced by a pointer or value of the implementation.
Below it's shown how to tag both types of references.
```go
type Idol struct {
  //noifgo:{Singer,p}
  singerPtr   Singer
  //noifgo:{Singer,v}
  singerValue Singer
}
```
If there are multiple references on the same line to be replaced as in a function signature it would be tagged as follows:
```go
//noifgo:{InterfaceA,p; InterfaceB,v; InterfaceC,p}
func MultipleReferences(aPtr InterfaceA, bValue InterfaceB, cPtr InterfaceC) error {
  ...
}
```
After tagging all the interface definitions and their references to replace, return to the folder containing the project's *main* package.
Instead of running *go build* like usual, use:
```
noifgo build
```
The resulting binary will most probably be more performant since the interfaces were replaced by their implementations when compiling the project.
Please note that *NoIFGo* backups your project files before making any changes and after the compilation finishes, *NoIFGo* restores the backuped files.

This way *NoIFGo* enables a project to fully utilise the power of interfaces without paying a penalty except for longer compilation times when running *NoIFGo*. During development and testing the standard Go tool is the recommended tool to use. *NoIFGo* should be used to produce a more optimized binary.

### Limitations
- Only one interface implementation may be defined in the project. If there are more NoIFGo returns an error. Test files are ignored, which means that interface implementations defined in test files do not count.
- If your package organisation has circular dependencies when replacing the interface references your project won't compile.

## Author

* **Tobias Strandberg** - *Initial work*

See also the list of [contributors](https://github.com/strtob01/noifgo/graphs/contributors) who participated in this project.

## License

This project is licensed under the Apache 2 License - see the [LICENSE.md](LICENSE.md) file for details

[go tools]: https://github.com/golang/tools
