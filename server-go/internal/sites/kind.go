package sites

// Site kinds (EXC-661): a static site is assets only; a server site also runs its build's Worker code.
const (
	KindStatic = "static"
	KindServer = "server"
)
