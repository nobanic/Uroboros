package binaryartifact

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const ContentProbeLimit = 1 << 20

var (
	ErrUnsupportedBinaryFormat = errors.New("unsupported binary format")
	ErrMalformedBinary         = errors.New("malformed binary header")
)

// Detection is the small, code-owned admission projection. It is deliberately
// independent from filenames and HTTP metadata; richer structural metadata is
// produced later by the isolated LIEF worker.
type Detection struct {
	Kind         string `json:"kind"`
	Format       string `json:"format"`
	Architecture string `json:"architecture"`
	Bitness      int    `json:"bitness,omitempty"`
	Endianness   string `json:"endianness"`
}

// Detect validates only bounded executable headers. prefix must contain at
// most ContentProbeLimit bytes from offset zero and totalSize must be the exact
// complete body size. Unknown formats fail closed and are never inferred from
// MIME, Content-Disposition, or a filename.
func Detect(prefix []byte, totalSize int64) (Detection, error) {
	if totalSize <= 0 || totalSize > 512<<20 || int64(len(prefix)) > totalSize || len(prefix) > ContentProbeLimit {
		return Detection{}, fmt.Errorf("%w: invalid probe bounds", ErrMalformedBinary)
	}
	if len(prefix) >= 2 && prefix[0] == 'M' && prefix[1] == 'Z' {
		return detectPE(prefix, totalSize)
	}
	if len(prefix) >= 4 && string(prefix[:4]) == "\x7fELF" {
		return detectELF(prefix, totalSize)
	}
	if len(prefix) >= 4 {
		if detection, matched, err := detectMachO(prefix, totalSize); matched {
			return detection, err
		}
	}
	return Detection{}, ErrUnsupportedBinaryFormat
}

func detectPE(prefix []byte, totalSize int64) (Detection, error) {
	if len(prefix) < 0x40 || totalSize < 0x40 {
		return Detection{}, fmt.Errorf("%w: truncated DOS header", ErrMalformedBinary)
	}
	peOffset := uint64(binary.LittleEndian.Uint32(prefix[0x3c:0x40]))
	if peOffset < 0x40 || peOffset > ContentProbeLimit-24 || peOffset+24 > uint64(totalSize) || peOffset+24 > uint64(len(prefix)) {
		return Detection{}, fmt.Errorf("%w: invalid PE header offset", ErrMalformedBinary)
	}
	base := int(peOffset)
	if string(prefix[base:base+4]) != "PE\x00\x00" {
		return Detection{}, fmt.Errorf("%w: missing PE signature", ErrMalformedBinary)
	}
	machine := binary.LittleEndian.Uint16(prefix[base+4 : base+6])
	sectionCount := binary.LittleEndian.Uint16(prefix[base+6 : base+8])
	optionalSize := binary.LittleEndian.Uint16(prefix[base+20 : base+22])
	if machine == 0 || sectionCount == 0 || sectionCount > 4096 || optionalSize < 2 {
		return Detection{}, fmt.Errorf("%w: invalid PE COFF header", ErrMalformedBinary)
	}
	optionalStart := uint64(base + 24)
	optionalEnd := optionalStart + uint64(optionalSize)
	sectionEnd := optionalEnd + uint64(sectionCount)*40
	if optionalEnd > uint64(totalSize) || optionalEnd > uint64(len(prefix)) || sectionEnd > uint64(totalSize) || sectionEnd > uint64(len(prefix)) {
		return Detection{}, fmt.Errorf("%w: truncated PE headers", ErrMalformedBinary)
	}
	for index := uint16(0); index < sectionCount; index++ {
		section := optionalEnd + uint64(index)*40
		rawSize := uint64(binary.LittleEndian.Uint32(prefix[section+16 : section+20]))
		rawOffset := uint64(binary.LittleEndian.Uint32(prefix[section+20 : section+24]))
		if rawSize > 0 && (rawOffset == 0 || rawOffset > uint64(totalSize) || rawSize > uint64(totalSize)-rawOffset) {
			return Detection{}, fmt.Errorf("%w: invalid PE section raw bounds", ErrMalformedBinary)
		}
	}
	magic := binary.LittleEndian.Uint16(prefix[optionalStart : optionalStart+2])
	detection := Detection{Kind: "native_binary", Format: "pe", Architecture: peArchitecture(machine), Endianness: "little"}
	var directoryOffset, directoryCountOffset uint64
	switch magic {
	case 0x10b:
		if optionalSize < 96 {
			return Detection{}, fmt.Errorf("%w: truncated PE32 optional header", ErrMalformedBinary)
		}
		detection.Bitness = 32
		directoryCountOffset = optionalStart + 92
		directoryOffset = optionalStart + 96
	case 0x20b:
		if optionalSize < 112 {
			return Detection{}, fmt.Errorf("%w: truncated PE32+ optional header", ErrMalformedBinary)
		}
		detection.Bitness = 64
		directoryCountOffset = optionalStart + 108
		directoryOffset = optionalStart + 112
	default:
		return Detection{}, fmt.Errorf("%w: unsupported PE optional header", ErrMalformedBinary)
	}
	// IMAGE_DIRECTORY_ENTRY_COM_DESCRIPTOR is index 14. Presence is used only
	// after the PE header and data-directory bounds above have been validated.
	directoryCount := uint32(0)
	if directoryCountOffset+4 <= optionalEnd {
		directoryCount = binary.LittleEndian.Uint32(prefix[directoryCountOffset : directoryCountOffset+4])
	}
	cliOffset := directoryOffset + 14*8
	if directoryCount > 14 && cliOffset+8 <= optionalEnd {
		rva := binary.LittleEndian.Uint32(prefix[cliOffset : cliOffset+4])
		size := binary.LittleEndian.Uint32(prefix[cliOffset+4 : cliOffset+8])
		if rva != 0 && size != 0 {
			if !peRVAMapped(prefix, optionalEnd, sectionCount, rva, size) {
				return Detection{}, fmt.Errorf("%w: invalid PE CLI directory bounds", ErrMalformedBinary)
			}
			detection.Kind = "managed_binary"
		}
	}
	return detection, nil
}

func peRVAMapped(prefix []byte, optionalEnd uint64, sectionCount uint16, rva, size uint32) bool {
	start, span := uint64(rva), uint64(size)
	for index := uint16(0); index < sectionCount; index++ {
		section := optionalEnd + uint64(index)*40
		virtualSize := uint64(binary.LittleEndian.Uint32(prefix[section+8 : section+12]))
		virtualAddress := uint64(binary.LittleEndian.Uint32(prefix[section+12 : section+16]))
		rawSize := uint64(binary.LittleEndian.Uint32(prefix[section+16 : section+20]))
		mappedSize := max(virtualSize, rawSize)
		if mappedSize > 0 && start >= virtualAddress && start-virtualAddress < mappedSize &&
			span <= mappedSize-(start-virtualAddress) {
			return true
		}
	}
	return false
}

func detectELF(prefix []byte, totalSize int64) (Detection, error) {
	if len(prefix) < 16 || totalSize < 16 {
		return Detection{}, fmt.Errorf("%w: truncated ELF identification", ErrMalformedBinary)
	}
	class, data, version := prefix[4], prefix[5], prefix[6]
	if (class != 1 && class != 2) || (data != 1 && data != 2) || version != 1 {
		return Detection{}, fmt.Errorf("%w: invalid ELF identification", ErrMalformedBinary)
	}
	headerSize := 52
	bitness := 32
	if class == 2 {
		headerSize = 64
		bitness = 64
	}
	if len(prefix) < headerSize || totalSize < int64(headerSize) {
		return Detection{}, fmt.Errorf("%w: truncated ELF header", ErrMalformedBinary)
	}
	order := binary.ByteOrder(binary.LittleEndian)
	endianness := "little"
	if data == 2 {
		order = binary.BigEndian
		endianness = "big"
	}
	machine := order.Uint16(prefix[18:20])
	ehsizeOffset := 0x28
	if class == 2 {
		ehsizeOffset = 0x34
	}
	if machine == 0 || int(order.Uint16(prefix[ehsizeOffset:ehsizeOffset+2])) != headerSize {
		return Detection{}, fmt.Errorf("%w: invalid ELF machine or header size", ErrMalformedBinary)
	}
	if err := validateELFTables(prefix, totalSize, class, order, headerSize); err != nil {
		return Detection{}, err
	}
	return Detection{
		Kind: "native_binary", Format: "elf", Architecture: elfArchitecture(machine),
		Bitness: bitness, Endianness: endianness,
	}, nil
}

func validateELFTables(prefix []byte, totalSize int64, class byte, order binary.ByteOrder, headerSize int) error {
	var programOffset, sectionOffset uint64
	var programEntrySize, programCount, sectionEntrySize, sectionCount uint16
	if class == 1 {
		programOffset = uint64(order.Uint32(prefix[28:32]))
		sectionOffset = uint64(order.Uint32(prefix[32:36]))
		programEntrySize, programCount = order.Uint16(prefix[42:44]), order.Uint16(prefix[44:46])
		sectionEntrySize, sectionCount = order.Uint16(prefix[46:48]), order.Uint16(prefix[48:50])
	} else {
		programOffset = order.Uint64(prefix[32:40])
		sectionOffset = order.Uint64(prefix[40:48])
		programEntrySize, programCount = order.Uint16(prefix[54:56]), order.Uint16(prefix[56:58])
		sectionEntrySize, sectionCount = order.Uint16(prefix[58:60]), order.Uint16(prefix[60:62])
	}
	if !validELFTable(programOffset, programEntrySize, programCount, uint64(headerSize), uint64(totalSize)) ||
		!validELFTable(sectionOffset, sectionEntrySize, sectionCount, uint64(headerSize), uint64(totalSize)) {
		return fmt.Errorf("%w: invalid ELF table bounds", ErrMalformedBinary)
	}
	return nil
}

func validELFTable(offset uint64, entrySize, count uint16, minimum, total uint64) bool {
	if count == 0 {
		return offset == 0 || (offset >= minimum && offset <= total)
	}
	if offset < minimum || entrySize == 0 || offset > total {
		return false
	}
	return uint64(count) <= (total-offset)/uint64(entrySize)
}

func detectMachO(prefix []byte, totalSize int64) (Detection, bool, error) {
	magic := binary.BigEndian.Uint32(prefix[:4])
	var order binary.ByteOrder
	bitness := 0
	format := "mach_o"
	fatEntrySize := 0
	switch magic {
	case 0xfeedface:
		order, bitness = binary.BigEndian, 32
	case 0xcefaedfe:
		order, bitness = binary.LittleEndian, 32
	case 0xfeedfacf:
		order, bitness = binary.BigEndian, 64
	case 0xcffaedfe:
		order, bitness = binary.LittleEndian, 64
	case 0xcafebabe:
		order, format, fatEntrySize = binary.BigEndian, "mach_o_fat", 20
	case 0xbebafeca:
		order, format, fatEntrySize = binary.LittleEndian, "mach_o_fat", 20
	case 0xcafebabf:
		order, format, fatEntrySize = binary.BigEndian, "mach_o_fat", 32
	case 0xbfbafeca:
		order, format, fatEntrySize = binary.LittleEndian, "mach_o_fat", 32
	default:
		return Detection{}, false, nil
	}
	endianness := "little"
	if order == binary.BigEndian {
		endianness = "big"
	}
	if fatEntrySize != 0 {
		if len(prefix) < 8 || totalSize < 8 {
			return Detection{}, true, fmt.Errorf("%w: truncated Mach-O fat header", ErrMalformedBinary)
		}
		count := order.Uint32(prefix[4:8])
		end := uint64(8) + uint64(count)*uint64(fatEntrySize)
		if count == 0 || count > 64 || end > uint64(totalSize) || end > uint64(len(prefix)) {
			return Detection{}, true, fmt.Errorf("%w: invalid Mach-O fat architecture table", ErrMalformedBinary)
		}
		for index := uint32(0); index < count; index++ {
			base := uint64(8) + uint64(index)*uint64(fatEntrySize)
			if order.Uint32(prefix[base:base+4]) == 0 {
				return Detection{}, true, fmt.Errorf("%w: invalid Mach-O fat CPU type", ErrMalformedBinary)
			}
			var offset, size uint64
			if fatEntrySize == 20 {
				offset = uint64(order.Uint32(prefix[base+8 : base+12]))
				size = uint64(order.Uint32(prefix[base+12 : base+16]))
			} else {
				offset = order.Uint64(prefix[base+8 : base+16])
				size = order.Uint64(prefix[base+16 : base+24])
			}
			if size == 0 || offset < end || offset > uint64(totalSize) || size > uint64(totalSize)-offset {
				return Detection{}, true, fmt.Errorf("%w: invalid Mach-O fat slice bounds", ErrMalformedBinary)
			}
		}
		return Detection{Kind: "native_binary", Format: format, Architecture: "universal", Endianness: endianness}, true, nil
	}
	headerSize := 28
	if bitness == 64 {
		headerSize = 32
	}
	if len(prefix) < headerSize || totalSize < int64(headerSize) {
		return Detection{}, true, fmt.Errorf("%w: truncated Mach-O header", ErrMalformedBinary)
	}
	cpuType := order.Uint32(prefix[4:8])
	commandCount := order.Uint32(prefix[16:20])
	commandBytes := order.Uint32(prefix[20:24])
	if cpuType == 0 || commandCount > 65535 || uint64(commandBytes) > uint64(totalSize)-uint64(headerSize) ||
		(commandCount == 0) != (commandBytes == 0) {
		return Detection{}, true, fmt.Errorf("%w: invalid Mach-O CPU type", ErrMalformedBinary)
	}
	return Detection{
		Kind: "native_binary", Format: format, Architecture: machArchitecture(cpuType),
		Bitness: bitness, Endianness: endianness,
	}, true, nil
}

func peArchitecture(machine uint16) string {
	switch machine {
	case 0x014c:
		return "x86"
	case 0x8664:
		return "x86_64"
	case 0x01c0, 0x01c2, 0x01c4:
		return "arm"
	case 0xaa64:
		return "arm64"
	case 0x0166, 0x0266, 0x0366, 0x0466:
		return "mips"
	default:
		return "unknown"
	}
}

func elfArchitecture(machine uint16) string {
	switch machine {
	case 3:
		return "x86"
	case 62:
		return "x86_64"
	case 40:
		return "arm"
	case 183:
		return "arm64"
	case 8:
		return "mips"
	case 20, 21:
		return "ppc"
	case 243:
		return "riscv"
	case 2, 18, 43:
		return "sparc"
	default:
		return "unknown"
	}
}

func machArchitecture(cpuType uint32) string {
	base := cpuType & 0x00ffffff
	switch base {
	case 7:
		if cpuType&0x01000000 != 0 {
			return "x86_64"
		}
		return "x86"
	case 12:
		if cpuType&0x01000000 != 0 {
			return "arm64"
		}
		return "arm"
	case 18:
		return "ppc"
	default:
		return "unknown"
	}
}
