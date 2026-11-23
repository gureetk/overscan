// SPDX-License-Identifier: AGPL-3.0-or-later

package streaminfo

import "errors"

// The video's parameters, from its sequence parameter set (H.264, ITU-T
// H.264 section 7.3.2.1.1; HEVC, ITU-T H.265 section 7.3.2.2) or sequence
// header (MPEG-2 video, ITU-T H.262 section 6.2.2.1). Only what the
// picture's size, its scan, its bit depth, its frame rate and its transfer
// characteristics need is read.

// picture is what a video's parameters say of its pictures.
type picture struct {
	width, height int
	interlaced    bool
	bitDepth      int
	transfer      int // the transfer characteristics (ITU-T H.273), 0 if not given
	frameRate     float64
}

var errBadSPS = errors.New("streaminfo: a parameter set out of range")

// h264HighProfiles are the profiles whose parameter sets carry chroma,
// bit depth and scaling matrices.
var h264HighProfiles = map[uint64]bool{100: true, 110: true, 122: true, 244: true, 44: true, 83: true, 86: true,
	118: true, 128: true, 138: true, 139: true, 134: true, 135: true}

// h264SPS reads an H.264 sequence parameter set: the NAL unit, from its
// header byte.
func h264SPS(nal []byte) (picture, error) {
	r := &bits{b: unescape(nal)}
	r.skip(8) // the NAL header
	profile := r.u(8)
	r.skip(16) // constraint flags and level
	r.ue()     // seq_parameter_set_id
	chroma, depth := uint64(1), uint64(0)
	if h264HighProfiles[profile] {
		chroma = r.ue()
		if chroma > 3 {
			return picture{}, errBadSPS
		}
		if chroma == 3 {
			r.skip(1) // separate_colour_plane_flag
		}
		depth = r.ue()
		r.ue()        // bit_depth_chroma_minus8
		r.skip(1)     // qpprime_y_zero_transform_bypass_flag
		if r.flag() { // seq_scaling_matrix_present_flag
			lists := 8
			if chroma == 3 {
				lists = 12
			}
			for i := range lists {
				if r.flag() {
					size := 16
					if i >= 6 {
						size = 64
					}
					h264ScalingList(r, size)
				}
			}
		}
	}
	r.ue()          // log2_max_frame_num_minus4
	switch r.ue() { // pic_order_cnt_type
	case 0:
		r.ue()
	case 1:
		r.skip(1)
		r.se()
		r.se()
		n := r.ue()
		if n > 255 {
			return picture{}, errBadSPS
		}
		for range n {
			r.se()
		}
	}
	r.ue()    // max_num_ref_frames
	r.skip(1) // gaps_in_frame_num_value_allowed_flag
	mbsWide := r.ue() + 1
	mapUnitsHigh := r.ue() + 1
	frameOnly := r.flag()
	if !frameOnly {
		r.skip(1) // mb_adaptive_frame_field_flag
	}
	r.skip(1) // direct_8x8_inference_flag
	var left, right, top, bottom uint64
	if r.flag() {
		left, right, top, bottom = r.ue(), r.ue(), r.ue(), r.ue()
	}
	if r.err != nil || mbsWide > 1024 || mapUnitsHigh > 1024 {
		return picture{}, errors.Join(errBadSPS, r.err)
	}
	fields := uint64(2)
	if frameOnly {
		fields = 1
	}
	cropX, cropY := uint64(1), fields
	switch chroma {
	case 1:
		cropX, cropY = 2, 2*fields
	case 2:
		cropX = 2
	}
	w := int64(mbsWide*16) - int64(cropX*(left+right))
	h := int64(fields*mapUnitsHigh*16) - int64(cropY*(top+bottom))
	if w <= 0 || h <= 0 {
		return picture{}, errBadSPS
	}
	p := picture{width: int(w), height: int(h), interlaced: !frameOnly, bitDepth: int(depth) + 8}
	if r.flag() { // vui_parameters_present_flag
		vui(r, &p, true)
	}
	return p, nil
}

// h264ScalingList skips a scaling list of the given size.
func h264ScalingList(r *bits, size int) {
	last, next := int64(8), int64(8)
	for range size {
		if next != 0 {
			next = (last + r.se() + 256) % 256
		}
		if next != 0 {
			last = next
		}
		if r.err != nil {
			return
		}
	}
}

// vui reads the video usability information as far as its colour
// description, and for H.264 its timing.
func vui(r *bits, p *picture, h264 bool) {
	if r.flag() { // aspect_ratio_info_present_flag
		if r.u(8) == 255 {
			r.skip(32)
		}
	}
	if r.flag() { // overscan_info_present_flag
		r.skip(1)
	}
	if r.flag() { // video_signal_type_present_flag
		r.skip(4) // video_format, video_full_range_flag
		if r.flag() {
			r.skip(8) // colour_primaries
			transfer := r.u(8)
			r.skip(8) // matrix_coefficients
			if r.err == nil {
				p.transfer = int(transfer)
			}
		}
	}
	if !h264 {
		return
	}
	if r.flag() { // chroma_loc_info_present_flag
		r.ue()
		r.ue()
	}
	if r.flag() { // timing_info_present_flag
		tick, scale := r.u(32), r.u(32)
		if r.err == nil && tick > 0 && scale > 0 {
			p.frameRate = float64(scale) / float64(2*tick)
		}
	}
}

// hevcSPS reads an HEVC sequence parameter set: the NAL unit, from its
// two header bytes.
func hevcSPS(nal []byte) (picture, error) {
	r := &bits{b: unescape(nal)}
	r.skip(16) // the NAL header
	r.skip(4)  // sps_video_parameter_set_id
	subLayers := int(r.u(3))
	r.skip(1) // sps_temporal_id_nesting_flag
	// profile_tier_level(1, subLayers)
	r.skip(2 + 1 + 5 + 32)
	progressive, interlacedSource := r.flag(), r.flag()
	r.skip(2 + 43 + 1)
	r.skip(8) // general_level_idc
	profilePresent := make([]bool, subLayers)
	levelPresent := make([]bool, subLayers)
	for i := range subLayers {
		profilePresent[i], levelPresent[i] = r.flag(), r.flag()
	}
	if subLayers > 0 {
		r.skip(2 * (8 - subLayers))
	}
	for i := range subLayers {
		if profilePresent[i] {
			r.skip(88)
		}
		if levelPresent[i] {
			r.skip(8)
		}
	}
	r.ue() // sps_seq_parameter_set_id
	chroma := r.ue()
	if chroma > 3 {
		return picture{}, errBadSPS
	}
	if chroma == 3 {
		r.skip(1) // separate_colour_plane_flag
	}
	w, h := r.ue(), r.ue()
	var left, right, top, bottom uint64
	if r.flag() { // conformance_window_flag
		left, right, top, bottom = r.ue(), r.ue(), r.ue(), r.ue()
	}
	depth := r.ue()
	r.ue() // bit_depth_chroma_minus8
	pocBits := r.ue() + 4
	if r.err != nil || w > 16384 || h > 16384 || pocBits > 16 {
		return picture{}, errors.Join(errBadSPS, r.err)
	}
	subX, subY := uint64(1), uint64(1)
	switch chroma {
	case 1:
		subX, subY = 2, 2
	case 2:
		subX = 2
	}
	width := int64(w) - int64(subX*(left+right))
	height := int64(h) - int64(subY*(top+bottom))
	if width <= 0 || height <= 0 {
		return picture{}, errBadSPS
	}
	p := picture{width: int(width), height: int(height), bitDepth: int(depth) + 8,
		interlaced: interlacedSource && !progressive}

	from := subLayers
	if r.flag() { // sps_sub_layer_ordering_info_present_flag
		from = 0
	}
	for i := from; i <= subLayers; i++ {
		r.ue()
		r.ue()
		r.ue()
	}
	for range 6 { // block and transform sizes, transform depths
		r.ue()
	}
	if r.flag() && r.flag() { // scaling_list_enabled_flag, sps_scaling_list_data_present_flag
		hevcScalingLists(r)
	}
	r.skip(2)     // amp_enabled_flag, sample_adaptive_offset_enabled_flag
	if r.flag() { // pcm_enabled_flag
		r.skip(8)
		r.ue()
		r.ue()
		r.skip(1)
	}
	sets := r.ue()
	if sets > 64 {
		return p, nil
	}
	deltas := make([]uint64, sets)
	for i := range int(sets) {
		if !hevcRefPicSet(r, i, deltas) {
			return p, nil
		}
	}
	if r.flag() { // long_term_ref_pics_present_flag
		n := r.ue()
		if n > 32 {
			return p, nil
		}
		for range n {
			r.skip(int(pocBits) + 1)
		}
	}
	r.skip(2) // sps_temporal_mvp_enabled_flag, strong_intra_smoothing_enabled_flag
	if r.err == nil && r.flag() {
		vui(r, &p, false)
	}
	return p, nil
}

// hevcScalingLists skips scaling_list_data().
func hevcScalingLists(r *bits) {
	for size := range 4 {
		step := 1
		if size == 3 {
			step = 3
		}
		for matrix := 0; matrix < 6; matrix += step {
			if !r.flag() {
				r.ue()
				continue
			}
			coefs := min(64, 1<<(4+size*2))
			if size > 1 {
				r.se()
			}
			for range coefs {
				r.se()
				if r.err != nil {
					return
				}
			}
		}
	}
}

// hevcRefPicSet skips st_ref_pic_set(idx) of a sequence parameter set,
// noting how many pictures it refers to in deltas; it reports false if it
// cannot.
func hevcRefPicSet(r *bits, idx int, deltas []uint64) bool {
	if idx > 0 && r.flag() { // inter_ref_pic_set_prediction_flag
		r.skip(1) // delta_rps_sign
		r.ue()    // abs_delta_rps_minus1
		var n uint64
		for range deltas[idx-1] + 1 {
			used := r.flag()
			if used || r.flag() {
				n++
			}
		}
		deltas[idx] = n
		return r.err == nil
	}
	neg, pos := r.ue(), r.ue()
	if neg > 16 || pos > 16 {
		return false
	}
	for range neg + pos {
		r.ue()
		r.skip(1)
	}
	deltas[idx] = neg + pos
	return r.err == nil
}

// mpeg2Sequence reads an MPEG-2 video sequence header, from its start
// code's last byte (0xB3).
func mpeg2Sequence(b []byte) (picture, error) {
	if len(b) < 8 {
		return picture{}, errShort
	}
	w := int(b[1])<<4 | int(b[2])>>4
	h := int(b[2]&0x0F)<<8 | int(b[3])
	if w == 0 || h == 0 {
		return picture{}, errBadSPS
	}
	rates := [...]float64{0, 24000.0 / 1001, 24, 25, 30000.0 / 1001, 30, 50, 60000.0 / 1001, 60}
	p := picture{width: w, height: h, bitDepth: 8}
	if code := int(b[4] & 0x0F); code < len(rates) {
		p.frameRate = rates[code]
	}
	return p, nil
}
