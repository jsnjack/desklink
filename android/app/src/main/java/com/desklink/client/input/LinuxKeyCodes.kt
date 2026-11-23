package com.desklink.client.input

import android.view.KeyEvent

/**
 * Android key events to Linux evdev KEY_* codes, which the host injects
 * through uinput. Hardware keyboards report the evdev code as the scan code;
 * the key-code table covers soft keyboards and synthetic keys.
 */
object LinuxKeyCodes {
    // The range the host registers on its virtual keyboard.
    const val MIN_KEY = 1
    const val MAX_KEY = 248

    // Codes from linux/input-event-codes.h used by name in the app.
    const val KEY_ESC = 1
    const val KEY_BACKSPACE = 14
    const val KEY_TAB = 15
    const val KEY_ENTER = 28
    const val KEY_LEFTCTRL = 29
    const val KEY_LEFTSHIFT = 42
    const val KEY_LEFTALT = 56
    const val KEY_SPACE = 57
    const val KEY_UP = 103
    const val KEY_LEFT = 105
    const val KEY_RIGHT = 106
    const val KEY_DOWN = 108
    const val KEY_LEFTMETA = 125
    const val KEY_A = 30
    const val KEY_SYSRQ = 99
    const val KEY_RIGHTSHIFT = 54
    const val KEY_RIGHTCTRL = 97
    const val KEY_RIGHTALT = 100
    const val KEY_RIGHTMETA = 126
    const val KEY_GRAVE = 41

    /** The scan code of Fn+Esc on keyboards that have no Esc of its own. */
    const val KEY_FN_ESC = 465

    /** Samsung's USB vendor ID, which its cover keyboards report. */
    private const val SAMSUNG_VENDOR = 0x04E8

    private val letters = mapOf(
        'a' to 30, 'b' to 48, 'c' to 46, 'd' to 32, 'e' to 18, 'f' to 33, 'g' to 34, 'h' to 35, 'i' to 23,
        'j' to 36, 'k' to 37, 'l' to 38, 'm' to 50, 'n' to 49, 'o' to 24, 'p' to 25, 'q' to 16, 'r' to 19,
        's' to 31, 't' to 20, 'u' to 22, 'v' to 47, 'w' to 17, 'x' to 45, 'y' to 21, 'z' to 44,
    )

    private val digits = intArrayOf(11, 2, 3, 4, 5, 6, 7, 8, 9, 10)

    /** Android key codes to evdev codes, for events without a hardware scan code. */
    val ANDROID_TO_LINUX: Map<Int, Int> = buildMap {
        for ((i, c) in ('a'..'z').withIndex()) put(KeyEvent.KEYCODE_A + i, letters.getValue(c))
        for (d in 0..9) put(KeyEvent.KEYCODE_0 + d, digits[d])
        for (f in 0..11) put(KeyEvent.KEYCODE_F1 + f, if (f < 10) 59 + f else 87 + (f - 10))
        put(KeyEvent.KEYCODE_ESCAPE, KEY_ESC)
        put(KeyEvent.KEYCODE_MINUS, 12)
        put(KeyEvent.KEYCODE_EQUALS, 13)
        put(KeyEvent.KEYCODE_DEL, KEY_BACKSPACE)
        put(KeyEvent.KEYCODE_TAB, KEY_TAB)
        put(KeyEvent.KEYCODE_LEFT_BRACKET, 26)
        put(KeyEvent.KEYCODE_RIGHT_BRACKET, 27)
        put(KeyEvent.KEYCODE_ENTER, KEY_ENTER)
        put(KeyEvent.KEYCODE_CTRL_LEFT, KEY_LEFTCTRL)
        put(KeyEvent.KEYCODE_SEMICOLON, 39)
        put(KeyEvent.KEYCODE_APOSTROPHE, 40)
        put(KeyEvent.KEYCODE_GRAVE, 41)
        put(KeyEvent.KEYCODE_SHIFT_LEFT, KEY_LEFTSHIFT)
        put(KeyEvent.KEYCODE_BACKSLASH, 43)
        put(KeyEvent.KEYCODE_COMMA, 51)
        put(KeyEvent.KEYCODE_PERIOD, 52)
        put(KeyEvent.KEYCODE_SLASH, 53)
        put(KeyEvent.KEYCODE_SHIFT_RIGHT, KEY_RIGHTSHIFT)
        put(KeyEvent.KEYCODE_NUMPAD_MULTIPLY, 55)
        put(KeyEvent.KEYCODE_ALT_LEFT, KEY_LEFTALT)
        put(KeyEvent.KEYCODE_SPACE, KEY_SPACE)
        put(KeyEvent.KEYCODE_CAPS_LOCK, 58)
        put(KeyEvent.KEYCODE_NUM_LOCK, 69)
        put(KeyEvent.KEYCODE_SCROLL_LOCK, 70)
        put(KeyEvent.KEYCODE_NUMPAD_7, 71)
        put(KeyEvent.KEYCODE_NUMPAD_8, 72)
        put(KeyEvent.KEYCODE_NUMPAD_9, 73)
        put(KeyEvent.KEYCODE_NUMPAD_SUBTRACT, 74)
        put(KeyEvent.KEYCODE_NUMPAD_4, 75)
        put(KeyEvent.KEYCODE_NUMPAD_5, 76)
        put(KeyEvent.KEYCODE_NUMPAD_6, 77)
        put(KeyEvent.KEYCODE_NUMPAD_ADD, 78)
        put(KeyEvent.KEYCODE_NUMPAD_1, 79)
        put(KeyEvent.KEYCODE_NUMPAD_2, 80)
        put(KeyEvent.KEYCODE_NUMPAD_3, 81)
        put(KeyEvent.KEYCODE_NUMPAD_0, 82)
        put(KeyEvent.KEYCODE_NUMPAD_DOT, 83)
        put(KeyEvent.KEYCODE_NUMPAD_ENTER, 96)
        put(KeyEvent.KEYCODE_CTRL_RIGHT, KEY_RIGHTCTRL)
        put(KeyEvent.KEYCODE_NUMPAD_DIVIDE, 98)
        put(KeyEvent.KEYCODE_SYSRQ, 99)
        put(KeyEvent.KEYCODE_ALT_RIGHT, KEY_RIGHTALT)
        put(KeyEvent.KEYCODE_MOVE_HOME, 102)
        put(KeyEvent.KEYCODE_DPAD_UP, KEY_UP)
        put(KeyEvent.KEYCODE_PAGE_UP, 104)
        put(KeyEvent.KEYCODE_DPAD_LEFT, KEY_LEFT)
        put(KeyEvent.KEYCODE_DPAD_RIGHT, KEY_RIGHT)
        put(KeyEvent.KEYCODE_MOVE_END, 107)
        put(KeyEvent.KEYCODE_DPAD_DOWN, KEY_DOWN)
        put(KeyEvent.KEYCODE_PAGE_DOWN, 109)
        put(KeyEvent.KEYCODE_INSERT, 110)
        put(KeyEvent.KEYCODE_FORWARD_DEL, 111)
        put(KeyEvent.KEYCODE_VOLUME_MUTE, 113)
        put(KeyEvent.KEYCODE_BREAK, 119)
        put(KeyEvent.KEYCODE_META_LEFT, KEY_LEFTMETA)
        put(KeyEvent.KEYCODE_META_RIGHT, KEY_RIGHTMETA)
        put(KeyEvent.KEYCODE_MENU, 127)
        put(KeyEvent.KEYCODE_MEDIA_NEXT, 163)
        put(KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE, 164)
        put(KeyEvent.KEYCODE_MEDIA_PREVIOUS, 165)
    }

    /**
     * The evdev code for a key event, or null for keys the host cannot inject.
     * Character keys send their scan code, so the host's keyboard layout
     * decides what they type. A key the keyboard's own layout turned into a
     * function key keeps that function. With [escFirst] the keyboard's
     * top-left key sends Esc and Fn with it types ` ([escFirstKeyboard]).
     */
    fun fromKeyEvent(scanCode: Int, keyCode: Int, escFirst: Boolean = false): Int? {
        if (escFirst) {
            when (scanCode) {
                KEY_GRAVE -> return KEY_ESC
                KEY_FN_ESC -> return KEY_GRAVE
            }
        }
        if (keyCode !in CHARACTER_KEYS) ANDROID_TO_LINUX[keyCode]?.let { return it }
        return if (scanCode in MIN_KEY..MAX_KEY) scanCode else ANDROID_TO_LINUX[keyCode]
    }

    /**
     * Whether a keyboard's Esc and ` are swapped for the desktop. Samsung's
     * Book Cover Keyboard Slim has no Esc key of its own: its top-left key
     * sends the scan code of ` (41) and gives Esc only with Fn (KEY_FN_ESC,
     * 465, which Samsung's layout makes Escape). Editors and terminals need
     * Esc far more often than `, so on that keyboard the plain key sends Esc
     * and Fn with it types `.
     */
    fun escFirstKeyboard(name: String?, vendorId: Int): Boolean =
        vendorId == SAMSUNG_VENDOR && name?.startsWith("Book Cover Keyboard Slim") == true

    /** Android key codes that type a character, whose meaning the host's layout decides. */
    private val CHARACTER_KEYS: Set<Int> = buildSet {
        addAll(KeyEvent.KEYCODE_0..KeyEvent.KEYCODE_9)
        addAll(KeyEvent.KEYCODE_A..KeyEvent.KEYCODE_Z)
        addAll(KeyEvent.KEYCODE_NUMPAD_0..KeyEvent.KEYCODE_NUMPAD_EQUALS)
        addAll(
            listOf(
                KeyEvent.KEYCODE_STAR, KeyEvent.KEYCODE_POUND, KeyEvent.KEYCODE_COMMA, KeyEvent.KEYCODE_PERIOD,
                KeyEvent.KEYCODE_SPACE, KeyEvent.KEYCODE_GRAVE, KeyEvent.KEYCODE_MINUS, KeyEvent.KEYCODE_EQUALS,
                KeyEvent.KEYCODE_LEFT_BRACKET, KeyEvent.KEYCODE_RIGHT_BRACKET, KeyEvent.KEYCODE_BACKSLASH,
                KeyEvent.KEYCODE_SEMICOLON, KeyEvent.KEYCODE_APOSTROPHE, KeyEvent.KEYCODE_SLASH, KeyEvent.KEYCODE_AT,
                KeyEvent.KEYCODE_PLUS,
            )
        )
    }

    private val unshifted = mapOf(
        ' ' to KEY_SPACE, '\n' to KEY_ENTER, '\t' to KEY_TAB, '-' to 12, '=' to 13, '[' to 26, ']' to 27,
        ';' to 39, '\'' to 40, '`' to 41, '\\' to 43, ',' to 51, '.' to 52, '/' to 53,
    )

    private val shifted = mapOf(
        '!' to 2, '@' to 3, '#' to 4, '$' to 5, '%' to 6, '^' to 7, '&' to 8, '*' to 9, '(' to 10, ')' to 11,
        '_' to 12, '+' to 13, '{' to 26, '}' to 27, ':' to 39, '"' to 40, '~' to 41, '|' to 43,
        '<' to 51, '>' to 52, '?' to 53,
    )

    /**
     * The key and whether Shift is needed to type [c] on a US layout, which
     * the host keyboard layout is assumed to be. Null for other characters.
     */
    fun forChar(c: Char): Pair<Int, Boolean>? = when {
        c in 'a'..'z' -> letters.getValue(c) to false
        c in 'A'..'Z' -> letters.getValue(c.lowercaseChar()) to true
        c in '0'..'9' -> digits[c - '0'] to false
        else -> unshifted[c]?.let { it to false } ?: shifted[c]?.let { it to true }
    }
}
