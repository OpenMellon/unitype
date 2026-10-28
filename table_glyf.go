/*
 * This file is licensed under the MIT License, as defined in
 * file 'LICENSE', which is part of this source code package.
 */

package unitype

import (
	"bytes"
	"errors"

	"github.com/sirupsen/logrus"
)

// glyfTable represents the Glyph Data table (glyf).
// Information that describes the glyphs in the font in the TrueType outline format.
//
// The 'glyf' table is comprised of a list of glyph data blocks, each of which provides
// the description for a single glyph. Glyphs are referenced by identifiers (glyph IDs),
// which are sequential integers beginning at zero. The total number of glyphs is specified
// by the numGlyphs field in the 'maxp' table. The 'glyf' table does not include any overall
// table header or records providing offsets to glyph data blocks. Rather, the 'loca' table
// provides an array of offsets, indexed by glyph IDs, which provide the location of each
// glyph data block within the 'glyf' table. Note that the 'glyf' table must always be used
// in conjunction with the 'loca' and 'maxp' tables.
// https://docs.microsoft.com/en-us/typography/opentype/spec/glyf
//
// NOTE: This table is usually the biggest by far, so avoid processing it unless necessary.
type glyfTable struct {
	descs []*glyphDescription
}

func (f *font) parseGlyf(r *byteReader) (*glyfTable, error) {
	if f.maxp == nil || f.loca == nil {
		logrus.Debug("required field missing (glyf)")
		return nil, errRequiredField
	}

	tr, has, err := f.seekToTable(r, "glyf")
	if err != nil {
		logrus.Debugf("ERROR: %v", err)
		return nil, err
	}
	if !has {
		return nil, nil // table not found.
	}

	glyf := &glyfTable{}

	logrus.Debug("parsing glyfs")
	logrus.Debugf("Number of glyphs: %d", f.maxp.numGlyphs)
	logrus.Debugf("Loca offset format: %d", f.head.indexToLocFormat)

	for i := 0; i < int(f.maxp.numGlyphs); i++ {
		gid := GlyphIndex(i)
		gdOffset, gdLen, err := f.GetGlyphDataOffset(gid)
		if err != nil {
			logrus.Debugf("ERROR: %v", err)
			return nil, err
		}

		if gdOffset > int64(tr.length) {
			logrus.Debugf("gid: %d, gdOffset: %d, tr len: %d, gd len: %d", gid, gdOffset, tr.length, gdLen)
			logrus.Debugf("Range check error (glyf): %d > %d", gdOffset, tr.length)
			return nil, errRangeCheck
		}

		err = r.SeekTo(int64(tr.offset) + gdOffset)
		if err != nil {
			logrus.Debugf("ERROR: %v", err)
			return nil, err
		}

		var desc glyphDescription
		desc.raw = make([]byte, gdLen)
		err = r.readBytes(&desc.raw, int(gdLen))
		if err != nil {
			logrus.Debugf("ERROR: %v", err)
			return nil, err
		}
		glyf.descs = append(glyf.descs, &desc)
	}

	return glyf, nil
}

type glyphDescription struct {
	raw []byte

	header    *glyphHeader
	composite *compositeGlyph
}

type glyphHeader struct {
	numberOfContours int16
	xMin             int16
	yMin             int16
	xMax             int16
	yMax             int16
}

// parse deserializes the glyph description data.
func (gd *glyphDescription) parse() error {
	if gd.header != nil {
		// Already loaded.
		return nil
	}

	r := newByteReader(bytes.NewReader(gd.raw))
	err := gd.parseHeader(r)
	if err != nil {
		logrus.Debugf("ERROR parsing header: %v", err)
		logrus.Debugf("Raw data: %d bytes", len(gd.raw))
		return err
	}

	if gd.IsSimple() {
		// TODO: Currently not loading the simple glyph description.
		//  Example code can be found lower in this file (commented out).
		return nil
	}

	return gd.parseComposite(r)
}

func (gd *glyphDescription) parseHeader(r *byteReader) error {
	var h glyphHeader
	err := r.read(&h.numberOfContours, &h.xMin, &h.yMin, &h.xMax, &h.yMax)
	if err != nil {
		return err
	}
	gd.header = &h
	return nil
}

func (gd *glyphDescription) parseComposite(r *byteReader) error {
	var composite compositeGlyph

	instructionsFollow := false
	for {
		var comp compositeComponent
		err := r.read(&comp.flags, &comp.glyphIndex)
		if err != nil {
			return err
		}

		flag := compositeGlyphFlag(comp.flags)
		if flag.IsSet(arg1And2AreWords) {
			err := r.read(&comp.argument1, &comp.argument2)
			if err != nil {
				return err
			}
		} else {
			var arg1, arg2 uint8
			err := r.read(&arg1, &arg2)
			if err != nil {
				return err
			}
			comp.argument1, comp.argument2 = uint16(arg1), uint16(arg2)
		}

		if flag.IsSet(weHaveAScale) {
			err := r.read(&comp.scale)
			if err != nil {
				return err
			}
		} else if flag.IsSet(weHaveAnXAndYScale) {
			err := r.read(&comp.scaleX, &comp.scaleY)
			if err != nil {
				return err
			}
		} else if flag.IsSet(weHaveATwoByTwo) {
			err := r.read(&comp.a, &comp.b, &comp.c, &comp.d)
			if err != nil {
				return err
			}
		}

		if !instructionsFollow && flag.IsSet(weHaveInstructions) {
			instructionsFollow = true
		}

		composite.components = append(composite.components, comp)
		if !flag.IsSet(moreComponents) {
			break
		}
	}

	if instructionsFollow {
		instructionLen := int64(len(gd.raw)) - r.Offset()
		if instructionLen <= 0 {
			logrus.Debug("Read more than length in loca table showed")
			return errors.New("no room for instructions")
		}
		err := r.readSlice(&composite.instructions, int(instructionLen))
		if err != nil {
			logrus.Debug("Failed to read instructions")
			return err
		}
	}

	gd.composite = &composite
	return nil
}

type compositeGlyph struct {
	components   []compositeComponent
	instructions []uint8
}

type compositeComponent struct {
	flags      uint16
	glyphIndex uint16
	argument1  uint16 // uint8, int8, uint16 or int16.
	argument2  uint16 // uint8, int8, uint16 or int16.

	// Optional transformation flags.
	scale          *f2dot14 // same scale for x and y.
	scaleX, scaleY *f2dot14 // x and y scales
	a, b, c, d     *f2dot14 // 2x2
}

type compositeGlyphFlag uint16

const (
	arg1And2AreWords compositeGlyphFlag = (1 << iota) // If set, the args are 16-bit (uint16/int16), otherwise uint8/int8.
	argsAreXYValues                                   // If set, the args are signed xy values (otherwise unsigned).
	roundXYToGrid
	weHaveAScale
	_              // reserved
	moreComponents // Indicates at least one glyph following this one.
	weHaveAnXAndYScale
	weHaveATwoByTwo
	weHaveInstructions
	useMyMetrics
	overlapCompound
	scaledComponentOffset
	unscaledComponentOffset
)

// IsSet checks if bit `flag` is set in `f`.
func (f compositeGlyphFlag) IsSet(flag compositeGlyphFlag) bool {
	return f&flag != 0
}

// Returns list of glyphs that `gid` depends on (other than itself).
func (glyf *glyfTable) GetComponents(gid GlyphIndex) ([]GlyphIndex, error) {
	if int(gid) >= len(glyf.descs) {
		logrus.Debugf("GID not accessible (%d > %d)", gid, len(glyf.descs))
		return nil, nil
	}

	var components []GlyphIndex
	gdesc := glyf.descs[int(gid)]

	if gdesc.header == nil {
		if len(gdesc.raw) == 0 {
			// No glyph data.
			return nil, nil
		}
		err := gdesc.parse()
		if err != nil {
			logrus.Debugf("ERROR parsing header: %v", err)
			return nil, err
		}
	}

	if gdesc.IsSimple() {
		return components, nil
	}
	if gdesc.composite == nil {
		logrus.Debugf("composite is nil")
		return components, nil
	}

	for _, comp := range gdesc.composite.components {
		components = append(components, GlyphIndex(comp.glyphIndex))
	}

	return components, nil
}

func (gd glyphDescription) IsSimple() bool {
	if gd.header == nil {
		err := gd.parse()
		if err != nil {
			logrus.Debugf("ERROR parsing header: %v", err)
			return true
		}
	}

	return gd.header.numberOfContours > -1
}

func (f *font) writeGlyf(w *byteWriter) error {
	if f.glyf == nil || f.maxp == nil || f.loca == nil {
		logrus.Debugf("glyf: required field missing (write)")
		return errRequiredField
	}

	if int(f.maxp.numGlyphs) != len(f.glyf.descs) {
		logrus.Debugf("Incorrect number of glyph descriptions")
		return errRangeCheck
	}

	for _, gd := range f.glyf.descs {
		err := w.writeBytes(gd.raw)
		if err != nil {
			return err
		}
	}

	return nil
}
