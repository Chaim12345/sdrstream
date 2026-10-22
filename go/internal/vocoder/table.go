// Code generated from sdrstream/_websdr_table.py. DO NOT EDIT.

package vocoder

// sampleTable maps an 8-bit stream byte to a signed 16-bit sample.
//
// WebSDR emits 128 samples from byte 0x80 by indexing this table. The table is
// an A-law-like companding curve that differs from ITU G.711 A-law in 32 of
// its 256 entries, so it is reproduced verbatim rather than computed.
//
// Values are from the public WebSDR HTML5 client (websdr-sound.js),
// copyright 2013-2025 PA3FWM. NO PERMISSION TO REDISTRIBUTE OR REUSE THIS TABLE
// HAS BEEN OBTAINED: upstream says the client code is "all rights reserved" and
// may only be served unmodified by the original WebSDR server to original
// clients, and that reusing parts of it in other software needs the author's
// explicit prior permission. TODO: obtain permission or replace the table
// (see NOTICE).
var sampleTable = [256]int16{
	-5504, -5248, -6016, -5760, -4480, -4224, -4992, -4736,
	-7552, -7296, -8064, -7808, -6528, -6272, -7040, -6784,
	-2752, -2624, -3008, -2880, -2240, -2112, -2496, -2368,
	-3776, -3648, -4032, -3904, -3264, -3136, -3520, -3392,
	-22016, -20992, -24064, -23040, -17920, -16896, -19968, -18944,
	-30208, -29184, -32256, -31232, -26112, -25088, -28160, -27136,
	-11008, -10496, -12032, -11520, -8960, -8448, -9984, -9472,
	-15104, -14592, -16128, -15616, -13056, -12544, -14080, -13568,
	-344, -328, -376, -360, -280, -264, -312, -296,
	-472, -456, -504, -488, -408, -392, -440, -424,
	-88, -72, -120, -104, -24, -8, -56, -40,
	-216, -200, -248, -232, -152, -136, -184, -168,
	-1376, -1312, -1504, -1440, -1120, -1056, -1248, -1184,
	-1888, -1824, -2016, -1952, -1632, -1568, -1760, -1696,
	-688, -656, -752, -720, -560, -528, -624, -592,
	-944, -912, -1008, -976, -816, -784, -880, -848,
	5504, 5248, 6016, 5760, 4480, 4224, 4992, 4736,
	7552, 7296, 8064, 7808, 6528, 6272, 7040, 6784,
	2752, 2624, 3008, 2880, 2240, 2112, 2496, 2368,
	3776, 3648, 4032, 3904, 3264, 3136, 3520, 3392,
	22016, 20992, 24064, 23040, 17920, 16896, 19968, 18944,
	30208, 29184, 32256, 31232, 26112, 25088, 28160, 27136,
	11008, 10496, 12032, 11520, 8960, 8448, 9984, 9472,
	15104, 14592, 16128, 15616, 13056, 12544, 14080, 13568,
	344, 328, 376, 360, 280, 264, 312, 296,
	472, 456, 504, 488, 408, 392, 440, 424,
	88, 72, 120, 104, 24, 8, 56, 40,
	216, 200, 248, 232, 152, 136, 184, 168,
	1376, 1312, 1504, 1440, 1120, 1056, 1248, 1184,
	1888, 1824, 2016, 1952, 1632, 1568, 1760, 1696,
	688, 656, 752, 720, 560, 528, 624, 592,
	944, 912, 1008, 976, 816, 784, 880, 848,
}
