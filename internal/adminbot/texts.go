package adminbot

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Every word the operator reads lives in this file. The bot's logic is
// language-neutral plumbing; what it says is Persian, is said in one
// place, and can be reworded without touching a single flow. The number
// and volume formatters live here too, for the same reason: how a
// quantity is written down is part of what the bot says.

// Buttons.
const (
	btnMyClients   = "👥 مشتری‌های من"
	btnAddClient   = "➕ مشتری جدید"
	btnSearch      = "🔍 جستجو"
	btnStats       = "📊 آمار من"
	btnHelp        = "❓ راهنما"
	btnBackToMenu  = "🏠 منوی اصلی"
	btnBackToList  = "🔙 بازگشت به فهرست"
	btnBack        = "🔙 بازگشت"
	btnCancel      = "❌ انصراف"
	btnPrev        = "⬅️ قبلی"
	btnNext        = "بعدی ➡️"
	btnSendLink    = "🔗 ارسال لینک اشتراک"
	btnCut         = "⛔ قطع کردن"
	btnRestore     = "▶️ وصل کردن"
	btnExtend      = "⏳ تمدید زمان"
	btnAddQuota    = "📦 افزایش حجم"
	btnResetUsage  = "🔄 ریست مصرف"
	btnDelete      = "🗑 حذف مشتری"
	btnConfirmRst  = "✅ بله، ریست شود"
	btnConfirmDel  = "🗑 بله، حذف شود"
	btnWizardNext  = "✅ ادامه"
	btnWizardMake  = "✅ ساخت مشتری"
	btnViewClient  = "👤 مشاهده مشتری"
	btnUnlimited   = "♾ نامحدود"
	btnCustom      = "✏️ مقدار دلخواه"
	btnDays7       = "➕ ۷ روز"
	btnDays30      = "➕ ۳۰ روز"
	btnGB10        = "➕ ۱۰ گیگ"
	btnGB50        = "➕ ۵۰ گیگ"
	btnTryAgain    = "🔁 تلاش دوباره"
	txtUnlimitedWd = "نامحدود"
)

// Menu and welcome.
const (
	txtWelcome   = "سلام! 👋\nبه بات مدیریت مشتری‌ها خوش آمدید.\nاز اینجا می‌توانید مشتری‌های خودتان را ببینید و مدیریت کنید."
	txtMenuTitle = "چه کاری می‌خواهید انجام دهید؟"
	txtError     = "مشکلی پیش آمد. لطفاً دوباره تلاش کنید."
	txtDoneToast = "انجام شد ✅"
)

// txtFailed is a failure with its reason attached: the service's own
// words, shown verbatim, because the operator is the one who has to act
// on them — a barred reseller is told why they are barred, not merely
// that something went wrong somewhere.
const txtFailed = "مشکلی پیش آمد. جزئیات:\n%s"

// txtNotOwner is the whole conversation a stranger gets: a management
// bot can do everything its operator can do to their customers, so a
// chat that is not the operator's named one is told what this bot is
// and nothing more — no menu, no names, no hint of whose it is.
const txtNotOwner = "این بات خصوصی است و فقط برای مدیر خودش کار می‌کند."

// The customer list.
const (
	txtListTitle = "👥 مشتری‌های من — %s مشتری"
	txtListPage  = "صفحهٔ %s از %s"
	txtNoClients = "هنوز مشتری‌ای ندارید.\nبا دکمهٔ «مشتری جدید» اولین مشتری را بسازید."
)

// One customer's detail.
const (
	txtDetailName          = "👤 <b>%s</b>"
	txtDetailStatus        = "وضعیت: %s %s"
	txtDetailUsed          = "📊 مصرف: %s از %s (%s٪)"
	txtDetailUsedUnlimited = "📊 مصرف: %s — حجم: نامحدود"
	txtDetailExpiry        = "⏰ انقضا: %s (%s)"
	txtDetailNoExpiry      = "⏰ انقضا: ندارد — نامحدود"
	txtDetailWaiting       = "⏰ شروع در اولین اتصال — مدت: %s"
	txtDetailDevices       = "📱 سقف دستگاه: %s"
	txtDetailTunnels       = "🛰 تونل‌ها: %s"
	txtDetailToken         = "🔑 توکن اشتراک:\n<code>%s</code>"
	txtClientGone          = "این مشتری دیگر وجود ندارد."
	txtStatusActive        = "فعال"
	txtStatusDisabled      = "قطع‌شده"
	txtStatusExpired       = "منقضی‌شده"
	txtStatusExhausted     = "حجم تمام‌شده"
	txtStatusOther         = "نامشخص"
	txtCutDone             = "مشتری قطع شد ⛔"
	txtRestoreDone         = "مشتری وصل شد ▶️"
)

// The subscription link, sent as its own message so the operator can
// forward it untouched. The token form is what is sent when the panel
// has no public address yet: the token alone still opens the
// subscription page when it is pasted after the panel's address.
const (
	txtLinkMessage = "🔗 لینک اشتراک «%s»:\n%s"
	txtLinkToken   = "🔑 توکن اشتراک «%s»:\n<code>%s</code>"
)

// Extending time and adding volume.
const (
	txtExtendTitle   = "⏳ تمدید زمان «%s»\nچند روز به اشتراکش اضافه شود؟"
	txtAskExtendDays = "تعداد روز را بفرستید (فقط عدد):"
	txtExtendNoDate  = "این مشتری انقضای زمانی ندارد؛ تمدید زمان برایش معنا ندارد."
	txtInvalidDays   = "عدد روز درست نیست. یک عدد بزرگ‌تر از صفر بفرستید:"
	txtQuotaTitle    = "📦 افزایش حجم «%s»\nچقدر به حجمش اضافه شود؟"
	txtAskQuotaGB    = "حجم را به گیگابایت بفرستید (فقط عدد):"
	txtQuotaNoCeil   = "حجم این مشتری نامحدود است؛ چیزی برای اضافه‌کردن نیست."
	txtInvalidGB     = "عدد حجم درست نیست. یک عدد بزرگ‌تر از صفر بفرستید:"
)

// Reset and delete ask first, because neither can be taken back.
const (
	txtResetAsk  = "🔄 مصرف «%s» صفر شود؟\nمصرف مشتری از اول شروع می‌شود و اگر حجمش تمام شده بود، دوباره فعال می‌شود."
	txtDeleteAsk = "🗑 مشتری «%s» کاملاً حذف شود؟\nاین کار برگشت ندارد."
	txtDeleted   = "🗑 مشتری «%s» حذف شد."
)

// The add wizard, five steps.
const (
	txtWizardAskName       = "➕ مشتری جدید\n\n۱ از ۵ — نام مشتری را بفرستید:"
	txtWizardNameEmpty     = "نام خالی قبول نیست. نام مشتری را بفرستید:"
	txtWizardPickTunnels   = "۲ از ۵ — تونل‌هایی را که این مشتری به آن‌ها وصل می‌شود انتخاب کنید و «ادامه» را بزنید:"
	txtWizardNeedTunnel    = "اول دست‌کم یک تونل انتخاب کنید."
	txtWizardNoTunnels     = "هیچ تونلی برای شما فعال نیست و مشتری بدون تونل ساخته نمی‌شود. با مدیر پنل صحبت کنید."
	txtWizardAskQuota      = "۳ از ۵ — حجم اشتراک چقدر باشد؟"
	txtWizardAskQuotaGB    = "حجم را به گیگابایت بفرستید (فقط عدد):"
	txtWizardAskDays       = "۴ از ۵ — مدت اشتراک چقدر باشد؟"
	txtWizardAskDaysCustom = "مدت را به روز بفرستید (فقط عدد):"
	txtWizardAskDevices    = "۵ از ۵ — سقف اتصال هم‌زمان چند دستگاه باشد؟"
	txtWizardSummary       = "📝 خلاصهٔ مشتری جدید:\n\n👤 نام: %s\n🛰 تونل‌ها: %s\n📦 حجم: %s\n⏰ مدت: %s\n📱 سقف دستگاه: %s"
	txtWizardCreated       = "✅ مشتری «%s» ساخته شد."
)

// Search.
const (
	txtSearchPrompt  = "🔍 بخشی از نام مشتری را بفرستید:"
	txtSearchNone    = "مشتری‌ای با این نام پیدا نشد."
	txtSearchResults = "🔍 نتایج جستجو (%s مشتری):"
)

// The operator's own totals.
const (
	txtStatsTitle          = "📊 آمار من"
	txtStatsTotal          = "کل مشتری‌ها: %s"
	txtStatsActive         = "🟢 فعال: %s"
	txtStatsDisabled       = "🔴 قطع‌شده: %s"
	txtStatsExpired        = "⏰ منقضی‌شده: %s"
	txtStatsExhausted      = "📛 حجم‌تمام‌شده: %s"
	txtStatsLimit          = "سقف من: %s از %s مشتری"
	txtStatsLimitUnlimited = "سقف من: نامحدود — تا امروز %s مشتری"
	txtStatsUsed           = "مجموع مصرف مشتری‌ها: %s"
)

// Help.
const txtHelp = "❓ راهنمای بات مدیریت\n\n" +
	"این بات فقط برای خودتان کار می‌کند و هر کاری که اینجا می‌کنید دقیقاً با دسترسی خودتان در پنل انجام می‌شود:\n\n" +
	"👥 مشتری‌های من — فهرست و جزئیات هر مشتری: ارسال لینک اشتراک، قطع و وصل، تمدید زمان و حجم، ریست مصرف و حذف.\n" +
	"➕ مشتری جدید — ساخت مشتری در پنج قدم.\n" +
	"🔍 جستجو — پیدا کردن مشتری با بخشی از نامش.\n" +
	"📊 آمار من — شمار مشتری‌ها و مجموع مصرفشان.\n\n" +
	"فرمان‌های /start و /cancel همیشه شما را به منوی اصلی برمی‌گردانند."

// faDigits writes Latin digits the way a Persian reader expects them.
func faDigits(s string) string {
	return strings.NewReplacer(
		"0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴",
		"5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹",
	).Replace(s)
}

// faNum writes a whole number grouped in thousands, in Persian digits.
func faNum(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	digits := fmt.Sprint(n)
	var groups []string
	for len(digits) > 3 {
		groups = append([]string{digits[len(digits)-3:]}, groups...)
		digits = digits[:len(digits)-3]
	}
	groups = append([]string{digits}, groups...)
	out := faDigits(strings.Join(groups, "٬"))
	if neg {
		return "-" + out
	}
	return out
}

// gbText is a volume in gigabytes, number and unit, with no opinion
// about zero: zero bytes is "۰ گیگابایت" here, because a customer's
// usage of nothing is a fact, not an absence of a ceiling.
func gbText(b uint64) string {
	gb := float64(b) / (1 << 30)
	if gb == float64(uint64(gb)) {
		return faNum(int64(gb)) + " گیگابایت"
	}
	return faDigits(fmt.Sprintf("%.1f", gb)) + " گیگابایت"
}

// formatBytes is a plan's volume: the panel's zero, which means no
// ceiling, says so instead of naming a number.
func formatBytes(b uint64) string {
	if b == 0 {
		return txtUnlimitedWd
	}
	return gbText(b)
}

// formatDays is a plan's length in days; no days is no limit.
func formatDays(days int) string {
	if days <= 0 {
		return txtUnlimitedWd
	}
	return faNum(int64(days)) + " روز"
}

// formatDate is a calendar date, compact and unambiguous.
func formatDate(t time.Time) string {
	return t.Format("2006/01/02")
}

// daysLeftText says how long is left before a date, in words.
func daysLeftText(until time.Time, now time.Time) string {
	d := until.Sub(now)
	if d <= 0 {
		return "به پایان رسیده"
	}
	days := int(d.Hours() / 24)
	if days == 0 {
		return "امروز تمام می‌شود"
	}
	return faNum(int64(days)) + " روز مانده"
}

// enDigits turns Persian and Arabic digits back into Latin ones, so a
// number the operator typed on a Persian keyboard parses like any
// other. Thousands separators go with them.
func enDigits(s string) string {
	return strings.NewReplacer(
		"۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4",
		"۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9",
		"٠", "0", "١", "1", "٢", "2", "٣", "3", "٤", "4",
		"٥", "5", "٦", "6", "٧", "7", "٨", "8", "٩", "9",
		"٬", "", ",", "",
	).Replace(s)
}

// parseAmount reads a number the operator typed, in either alphabet,
// whole or fractional. Zero, negatives and text are not amounts.
func parseAmount(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(enDigits(s)), 64)
	if err != nil || f <= 0 {
		return 0, false
	}
	return f, true
}
