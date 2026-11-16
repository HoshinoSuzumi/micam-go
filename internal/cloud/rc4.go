package cloud

// rc4Drop is how many keystream bytes Xiaomi's cloud API discards. It is not
// optional and the standard library gives no way to skip keystream, which is
// why RC4 is hand-rolled here rather than imported.
const rc4Drop = 1024

// RC4 applies the drop-1024 RC4 variant used by the signed cloud API.
// Encryption and decryption are the same operation.
func RC4(key, data []byte) []byte {
	if len(key) == 0 {
		return nil
	}

	var s [256]byte
	for i := range s {
		s[i] = byte(i)
	}
	j := 0
	for i := 0; i < 256; i++ {
		j = (j + int(s[i]) + int(key[i%len(key)])) & 0xFF
		s[i], s[j] = s[j], s[i]
	}

	i := 0
	j = 0
	next := func() byte {
		i = (i + 1) & 0xFF
		j = (j + int(s[i])) & 0xFF
		s[i], s[j] = s[j], s[i]
		return s[(int(s[i])+int(s[j]))&0xFF]
	}
	for k := 0; k < rc4Drop; k++ {
		next()
	}

	out := make([]byte, len(data))
	for k, b := range data {
		out[k] = b ^ next()
	}
	return out
}
