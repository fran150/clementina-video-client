package input

import (
	"math"

	"github.com/fran150/clementina-video-client/internal/mia"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

type Config struct {
	Keyboard bool
	Mouse    bool
	Gamepads bool
}

type KeyBlocker func(ebiten.Key) bool

type Forwarder struct {
	client *mia.InputClient
	cfg    Config

	connected bool

	keysBuf      []ebiten.Key
	charsBuf     []rune
	textBytesBuf []byte
	keyboard     [32]byte
	consumer     [32]byte

	mouseReady     bool
	mouseX         int
	mouseY         int
	mouseButtons   uint8
	wheelRemainder float64
	panRemainder   float64

	gamepadIDs      []ebiten.GamepadID
	gamepadSlots    map[ebiten.GamepadID]uint8
	gamepadSlotUsed [4]bool
	gamepadState    [4][10]byte
}

func NewForwarder(client *mia.InputClient, cfg Config) *Forwarder {
	return &Forwarder{
		client:       client,
		cfg:          cfg,
		gamepadSlots: map[ebiten.GamepadID]uint8{},
	}
}

func (f *Forwarder) Update(blocked KeyBlocker, mouseCaptured bool) {
	if f.client == nil {
		return
	}

	connected := f.client.Connected()
	if connected && !f.connected {
		f.Sync()
	}
	f.connected = connected

	if f.cfg.Keyboard {
		f.updateKeyboard(blocked)
	}
	if f.cfg.Mouse {
		f.updateMouse(mouseCaptured)
	}
	if f.cfg.Gamepads {
		f.updateGamepads()
	}
}

func (f *Forwarder) Sync() {
	if f.client == nil || !f.client.Connected() {
		return
	}
	if f.cfg.Keyboard {
		f.client.SendHIDBitmap(mia.HIDPageKeyboard, f.keyboard)
		f.client.SendHIDBitmap(mia.HIDPageConsumer, f.consumer)
	}
	if f.cfg.Gamepads {
		for player := uint8(0); player < 4; player++ {
			if f.gamepadSlotUsed[player] {
				f.client.SendGamepadState(player, f.gamepadState[player])
			}
		}
	}
}

func (f *Forwarder) ClearKeyboard() {
	clear(f.keyboard[:])
	clear(f.consumer[:])
	if f.client != nil {
		f.client.ClearState(mia.ClearKeyboard | mia.ClearConsumer)
	}
}

func (f *Forwarder) ClearMouse() {
	f.mouseReady = false
	f.mouseButtons = 0
	f.wheelRemainder = 0
	f.panRemainder = 0
	if f.client != nil {
		f.client.ClearState(mia.ClearMouse)
	}
}

func (f *Forwarder) ClearAll() {
	clear(f.keyboard[:])
	clear(f.consumer[:])
	f.mouseReady = false
	f.mouseButtons = 0
	f.wheelRemainder = 0
	f.panRemainder = 0
	for id := range f.gamepadSlots {
		delete(f.gamepadSlots, id)
	}
	clear(f.gamepadSlotUsed[:])
	clear(f.gamepadState[:])
	if f.client != nil {
		f.client.ClearState(mia.ClearKeyboard | mia.ClearConsumer | mia.ClearMouse | mia.ClearGamepads)
	}
}

func (f *Forwarder) updateKeyboard(blocked KeyBlocker) {
	f.keysBuf = inpututil.AppendJustPressedKeys(f.keysBuf[:0])
	for _, key := range f.keysBuf {
		if blocked != nil && blocked(key) {
			continue
		}
		f.applyKey(key, true)
	}

	f.keysBuf = inpututil.AppendJustReleasedKeys(f.keysBuf[:0])
	for _, key := range f.keysBuf {
		if blocked != nil && blocked(key) {
			continue
		}
		f.applyKey(key, false)
	}

	f.charsBuf = ebiten.AppendInputChars(f.charsBuf[:0])
	f.textBytesBuf = appendMIABytes(f.textBytesBuf[:0], f.charsBuf)
	if len(f.textBytesBuf) != 0 {
		f.client.SendText(f.textBytesBuf)
	}
}

func (f *Forwarder) applyKey(key ebiten.Key, down bool) {
	usage, ok := keyboardUsage(key)
	if !ok {
		return
	}

	if !setBitmapBit(&f.keyboard, usage, down) {
		return
	}
	// Printable characters are delivered as text via ebiten.AppendInputChars
	// (which applies the keyboard layout, Shift, and IME). Control keys such as
	// Enter, Tab, and Backspace are not reported as input characters, so their
	// text byte is attached to the HID key event instead. MIA enqueues this text
	// only on key-down.
	f.client.SendHIDEvent(mia.HIDPageKeyboard, usage, down, false, controlKeyText(key))
}

// controlKeyText returns the MIA text byte for keys that produce text but are
// not reported by ebiten.AppendInputChars. Keys with no associated text (or
// whose text comes from AppendInputChars) return 0.
func controlKeyText(key ebiten.Key) uint8 {
	switch key {
	case ebiten.KeyEnter, ebiten.KeyNumpadEnter:
		return 0x0D
	case ebiten.KeyTab:
		return 0x09
	case ebiten.KeyBackspace:
		return 0x08
	case ebiten.KeyEscape:
		return 0x1B
	default:
		return 0
	}
}

func (f *Forwarder) updateMouse(mouseCaptured bool) {
	x, y := ebiten.CursorPosition()
	buttons := mouseButtonBits()
	wheelX, wheelY := ebiten.Wheel()

	if !mouseCaptured {
		f.mouseReady = false
		f.mouseX = x
		f.mouseY = y
		f.wheelRemainder = 0
		f.panRemainder = 0
		return
	}

	if !f.mouseReady {
		f.mouseReady = true
		f.mouseX = x
		f.mouseY = y
		f.mouseButtons = buttons
		if buttons != 0 {
			f.client.SendMouseDelta(buttons, 0, 0, 0, 0)
		}
		return
	}

	dx := x - f.mouseX
	dy := y - f.mouseY
	f.mouseX = x
	f.mouseY = y

	f.panRemainder += wheelX
	f.wheelRemainder += wheelY
	pan := takeWholeWheel(&f.panRemainder)
	wheel := takeWholeWheel(&f.wheelRemainder)

	if dx == 0 && dy == 0 && wheel == 0 && pan == 0 && buttons == f.mouseButtons {
		return
	}

	f.sendMouseDeltas(buttons, dx, dy, wheel, pan)
	f.mouseButtons = buttons
}

func (f *Forwarder) sendMouseDeltas(buttons uint8, dx int, dy int, wheel int, pan int) {
	sentDelta := false
	for dx != 0 || dy != 0 || wheel != 0 || pan != 0 {
		stepX := takeInt8Step(&dx)
		stepY := takeInt8Step(&dy)
		stepWheel := takeInt8Step(&wheel)
		stepPan := takeInt8Step(&pan)
		f.client.SendMouseDelta(buttons, stepX, stepY, stepWheel, stepPan)
		sentDelta = true
	}
	if buttons != f.mouseButtons && !sentDelta {
		f.client.SendMouseDelta(buttons, 0, 0, 0, 0)
	}
}

func (f *Forwarder) updateGamepads() {
	f.gamepadIDs = ebiten.AppendGamepadIDs(f.gamepadIDs[:0])
	present := map[ebiten.GamepadID]struct{}{}
	for _, id := range f.gamepadIDs {
		present[id] = struct{}{}
		if _, ok := f.gamepadSlots[id]; !ok {
			f.assignGamepadSlot(id)
		}
	}

	for id, player := range f.gamepadSlots {
		if _, ok := present[id]; ok {
			continue
		}
		delete(f.gamepadSlots, id)
		f.gamepadSlotUsed[player] = false
		clear(f.gamepadState[player][:])
		f.client.ClearGamepad(player)
	}

	for _, id := range f.gamepadIDs {
		player, ok := f.gamepadSlots[id]
		if !ok {
			continue
		}
		next := buildGamepadState(id)
		if next == f.gamepadState[player] {
			continue
		}
		f.gamepadState[player] = next
		f.client.SendGamepadState(player, next)
	}
}

func (f *Forwarder) assignGamepadSlot(id ebiten.GamepadID) {
	for player := uint8(0); player < 4; player++ {
		if f.gamepadSlotUsed[player] {
			continue
		}
		f.gamepadSlots[id] = player
		f.gamepadSlotUsed[player] = true
		f.gamepadState[player] = [10]byte{}
		return
	}
}

func setBitmapBit(bitmap *[32]byte, usage uint16, down bool) bool {
	if usage > 0xFF {
		return false
	}
	byteIndex := usage >> 3
	mask := byte(1 << (usage & 7))
	wasDown := bitmap[byteIndex]&mask != 0
	if wasDown == down {
		return false
	}
	if down {
		bitmap[byteIndex] |= mask
	} else {
		bitmap[byteIndex] &^= mask
	}
	return true
}

func appendMIABytes(dst []byte, chars []rune) []byte {
	// Only printable characters arrive here; ebiten.AppendInputChars does not
	// report control keys (Enter, Tab, Backspace, ...). Their text bytes are
	// sent with the HID key event instead (see controlKeyText), so there is a
	// single source of text per key and no double enqueue.
	for _, r := range chars {
		if r >= 0x20 && r <= 0x7E {
			dst = append(dst, byte(r))
		}
	}
	return dst
}

func mouseButtonBits() uint8 {
	var buttons uint8
	if ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft) {
		buttons |= 1 << 0
	}
	if ebiten.IsMouseButtonPressed(ebiten.MouseButtonRight) {
		buttons |= 1 << 1
	}
	if ebiten.IsMouseButtonPressed(ebiten.MouseButtonMiddle) {
		buttons |= 1 << 2
	}
	if ebiten.IsMouseButtonPressed(ebiten.MouseButton3) {
		buttons |= 1 << 3
	}
	if ebiten.IsMouseButtonPressed(ebiten.MouseButton4) {
		buttons |= 1 << 4
	}
	return buttons
}

func takeWholeWheel(value *float64) int {
	if math.Abs(*value) < 1 {
		return 0
	}
	whole := int(*value)
	*value -= float64(whole)
	return whole
}

func takeInt8Step(value *int) int8 {
	if *value > 127 {
		*value -= 127
		return 127
	}
	if *value < -128 {
		*value += 128
		return -128
	}
	step := int8(*value)
	*value = 0
	return step
}

func buildGamepadState(id ebiten.GamepadID) [10]byte {
	var state [10]byte
	state[0] = 0x80

	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonLeftTop) {
		state[0] |= 1 << 0
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonLeftBottom) {
		state[0] |= 1 << 1
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonLeftLeft) {
		state[0] |= 1 << 2
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonLeftRight) {
		state[0] |= 1 << 3
	}

	lx := axisToInt8(ebiten.StandardGamepadAxisValue(id, ebiten.StandardGamepadAxisLeftStickHorizontal))
	ly := axisToInt8(ebiten.StandardGamepadAxisValue(id, ebiten.StandardGamepadAxisLeftStickVertical))
	rx := axisToInt8(ebiten.StandardGamepadAxisValue(id, ebiten.StandardGamepadAxisRightStickHorizontal))
	ry := axisToInt8(ebiten.StandardGamepadAxisValue(id, ebiten.StandardGamepadAxisRightStickVertical))
	state[4] = byte(lx)
	state[5] = byte(ly)
	state[6] = byte(rx)
	state[7] = byte(ry)

	if ly < -64 {
		state[1] |= 1 << 0
	}
	if ly > 64 {
		state[1] |= 1 << 1
	}
	if lx < -64 {
		state[1] |= 1 << 2
	}
	if lx > 64 {
		state[1] |= 1 << 3
	}
	if ry < -64 {
		state[1] |= 1 << 4
	}
	if ry > 64 {
		state[1] |= 1 << 5
	}
	if rx < -64 {
		state[1] |= 1 << 6
	}
	if rx > 64 {
		state[1] |= 1 << 7
	}

	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightBottom) {
		state[2] |= 1 << 0
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightRight) {
		state[2] |= 1 << 1
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightLeft) {
		state[2] |= 1 << 3
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightTop) {
		state[2] |= 1 << 4
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonFrontTopLeft) {
		state[2] |= 1 << 6
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonFrontTopRight) {
		state[2] |= 1 << 7
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonFrontBottomLeft) {
		state[3] |= 1 << 0
		state[8] = 0xFF
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonFrontBottomRight) {
		state[3] |= 1 << 1
		state[9] = 0xFF
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonCenterLeft) {
		state[3] |= 1 << 2
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonCenterRight) {
		state[3] |= 1 << 3
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonCenterCenter) {
		state[3] |= 1 << 4
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonLeftStick) {
		state[3] |= 1 << 5
	}
	if ebiten.IsStandardGamepadButtonPressed(id, ebiten.StandardGamepadButtonRightStick) {
		state[3] |= 1 << 6
	}

	return state
}

func axisToInt8(value float64) int8 {
	if value > 1 {
		value = 1
	} else if value < -1 {
		value = -1
	}
	if value >= 0 {
		return int8(value * 127)
	}
	return int8(value * 128)
}

func keyboardUsage(key ebiten.Key) (uint16, bool) {
	if key >= ebiten.KeyA && key <= ebiten.KeyZ {
		return uint16(key-ebiten.KeyA) + 0x04, true
	}
	if key >= ebiten.KeyDigit1 && key <= ebiten.KeyDigit9 {
		return uint16(key-ebiten.KeyDigit1) + 0x1E, true
	}
	if key == ebiten.KeyDigit0 {
		return 0x27, true
	}
	if key >= ebiten.KeyF1 && key <= ebiten.KeyF12 {
		return uint16(key-ebiten.KeyF1) + 0x3A, true
	}
	if key >= ebiten.KeyF13 && key <= ebiten.KeyF24 {
		return uint16(key-ebiten.KeyF13) + 0x68, true
	}
	if key >= ebiten.KeyNumpad1 && key <= ebiten.KeyNumpad9 {
		return uint16(key-ebiten.KeyNumpad1) + 0x59, true
	}

	switch key {
	case ebiten.KeyEnter:
		return 0x28, true
	case ebiten.KeyEscape:
		return 0x29, true
	case ebiten.KeyBackspace:
		return 0x2A, true
	case ebiten.KeyTab:
		return 0x2B, true
	case ebiten.KeySpace:
		return 0x2C, true
	case ebiten.KeyMinus:
		return 0x2D, true
	case ebiten.KeyEqual:
		return 0x2E, true
	case ebiten.KeyBracketLeft:
		return 0x2F, true
	case ebiten.KeyBracketRight:
		return 0x30, true
	case ebiten.KeyBackslash:
		return 0x31, true
	case ebiten.KeySemicolon:
		return 0x33, true
	case ebiten.KeyQuote:
		return 0x34, true
	case ebiten.KeyBackquote:
		return 0x35, true
	case ebiten.KeyComma:
		return 0x36, true
	case ebiten.KeyPeriod:
		return 0x37, true
	case ebiten.KeySlash:
		return 0x38, true
	case ebiten.KeyCapsLock:
		return 0x39, true
	case ebiten.KeyPrintScreen:
		return 0x46, true
	case ebiten.KeyScrollLock:
		return 0x47, true
	case ebiten.KeyPause:
		return 0x48, true
	case ebiten.KeyInsert:
		return 0x49, true
	case ebiten.KeyHome:
		return 0x4A, true
	case ebiten.KeyPageUp:
		return 0x4B, true
	case ebiten.KeyDelete:
		return 0x4C, true
	case ebiten.KeyEnd:
		return 0x4D, true
	case ebiten.KeyPageDown:
		return 0x4E, true
	case ebiten.KeyArrowRight:
		return 0x4F, true
	case ebiten.KeyArrowLeft:
		return 0x50, true
	case ebiten.KeyArrowDown:
		return 0x51, true
	case ebiten.KeyArrowUp:
		return 0x52, true
	case ebiten.KeyNumLock:
		return 0x53, true
	case ebiten.KeyNumpadDivide:
		return 0x54, true
	case ebiten.KeyNumpadMultiply:
		return 0x55, true
	case ebiten.KeyNumpadSubtract:
		return 0x56, true
	case ebiten.KeyNumpadAdd:
		return 0x57, true
	case ebiten.KeyNumpadEnter:
		return 0x58, true
	case ebiten.KeyNumpad0:
		return 0x62, true
	case ebiten.KeyNumpadDecimal:
		return 0x63, true
	case ebiten.KeyIntlBackslash:
		return 0x64, true
	case ebiten.KeyContextMenu:
		return 0x65, true
	case ebiten.KeyNumpadEqual:
		return 0x67, true
	case ebiten.KeyControlLeft:
		return 0xE0, true
	case ebiten.KeyShiftLeft:
		return 0xE1, true
	case ebiten.KeyAltLeft:
		return 0xE2, true
	case ebiten.KeyMetaLeft:
		return 0xE3, true
	case ebiten.KeyControlRight:
		return 0xE4, true
	case ebiten.KeyShiftRight:
		return 0xE5, true
	case ebiten.KeyAltRight:
		return 0xE6, true
	case ebiten.KeyMetaRight:
		return 0xE7, true
	default:
		return 0, false
	}
}
