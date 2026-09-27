package node

// Both implementations offer every operation.
var (
	_ Node = (*Local)(nil)
	_ Node = (*Remote)(nil)

	_ Mover = (*Local)(nil)
	_ Mover = (*Remote)(nil)
)
