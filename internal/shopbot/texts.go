package shopbot

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/shop"
)

// Every word the buyer reads lives in this file. The bot's logic is
// language-neutral plumbing; what it says is Persian, is said in one
// place, and can be reworded without touching a single flow.

// Buttons.
const (
	btnBuy      = "🛒 خرید اشتراک"
	btnMine     = "📊 اشتراک من"
	btnSupport  = "💬 پشتیبانی"
	btnRenew    = "♻️ تمدید اشتراک"
	btnBack     = "🔙 بازگشت"
	btnPayCard  = "💳 کارت‌به‌کارت"
	btnPayLink  = "🌐 پرداخت آنلاین"
	btnPaid     = "✅ پرداخت کردم"
	btnApprove  = "✅ تأیید پرداخت"
	btnReject   = "❌ رد کردن"
	txtBackWord = "بازگشت"
)

// Menu and welcome.
const (
	txtWelcomeDefault = "سلام! 👋\nبه فروشگاه اشتراک خوش آمدید.\nاز منوی زیر می‌توانید اشتراک بخرید، اشتراکتان را ببینید یا با پشتیبانی در تماس باشید."
	txtMenuTitle      = "چه کاری می‌خواهید انجام دهید؟"
	txtError          = "مشکلی پیش آمد. لطفاً دوباره تلاش کنید."
)

// Buying.
const (
	txtChoosePlan       = "یکی از پلن‌های زیر را انتخاب کنید:"
	txtNoPlans          = "در حال حاضر پلنی برای فروش فعال نیست. لطفاً بعداً دوباره سر بزنید."
	txtChoosePay        = "روش پرداخت را انتخاب کنید:"
	txtPayNotConfigured = "فروشنده هنوز روش پرداختی فعال نکرده است. لطفاً از پشتیبانی پیگیری کنید."
	txtOrderFailed      = "ساختن سفارش انجام نشد. لطفاً دوباره تلاش کنید."
)

// Sections. The shop sells two different products -- the panel's own
// WireGuard tunnels and the 3x-ui inbounds -- and the buyer picks which
// one they are shopping for before they see a single plan, because the
// two live in different apps and a plan list mixing them reads as one
// product with two names.
const (
	btnSectionWireGuard = "🛡 وایرگارد"
	btnSectionV2ray     = "🚀 V2ray"
	txtChooseSection    = "اشتراک کدام بخش را می‌خواهید؟"
)

// txtChoosePlanSection heads a section's plan list, naming the section
// the buyer just chose, so the list never has to be guessed at.
const txtChoosePlanSection = "📦 بخش %s\n\n" + txtChoosePlan

// sectionName is a section's name as the buyer sees it -- the same
// words on the choice button and at the head of its plan list.
func sectionName(section string) string {
	if shop.NormalizeSection(section) == shop.SectionV2ray {
		return btnSectionV2ray
	}
	return btnSectionWireGuard
}

// Card payment.
const txtCardInstructions = "برای پرداخت، مبلغ زیر را به این شماره کارت واریز کنید و سپس <b>عکس رسید</b> را همین‌جا بفرستید:\n\n" +
	"💳 شماره کارت:\n<code>%s</code>\n" +
	"👤 به نام: %s\n" +
	"💰 مبلغ: %s\n\n" +
	"بعد از ارسال رسید، پرداخت شما بررسی و اشتراکتان فعال می‌شود."

const (
	txtReceiptAskPhoto = "لطفاً عکس رسید پرداخت را بفرستید. اگر منصرف شده‌اید، «بازگشت» را بزنید."
	txtReceiptReceived = "رسید شما رسید ✅\nپرداخت در حال بررسی است؛ به محض تأیید، اشتراکتان همین‌جا تحویل داده می‌شود."
	txtReceiptCaption  = "🧾 رسید پرداخت\n\n👤 خریدار: %s\n🆔 آیدی: <code>%d</code>\n📦 پلن: %s\n💰 مبلغ: %s\n🔢 شماره سفارش: %d"
	txtApprovedSeller  = "✅ سفارش %d تأیید و اشتراک تحویل خریدار شد."
	txtRejectedSeller  = "❌ سفارش %d رد شد و به خریدار خبر داده شد."
	txtRejectedBuyer   = "متأسفانه پرداخت سفارش شما تأیید نشد. اگر اشتباهی شده، از پشتیبانی پیام بدهید تا بررسی شود."
	txtDecisionFailed  = "انجام این کار نشد؛ سفارش احتمالاً قبلاً تعیین تکلیف شده است."
	txtNotAllowed      = "این دکمه برای شما نیست."
	txtOrderGone       = "این سفارش دیگر در دسترس نیست."
)

// ZarinPal payment.
const txtZarinpalDesc = "خرید اشتراک %s"

const (
	txtZarinpalReady  = "سفارش شما ساخته شد. برای پرداخت روی دکمهٔ زیر بزنید؛ بعد از پرداخت به همین‌جا برگردید و «پرداخت کردم» را بزنید تا اشتراکتان تحویل شود."
	txtZarinpalFailed = "درگاه پرداخت الان پاسخ نمی‌دهد. لطفاً چند دقیقهٔ دیگر دوباره تلاش کنید."
	txtNotPaidYet     = "پرداخت شما هنوز در درگاه تأیید نشده است. اگر تازه پرداخت کرده‌اید، چند لحظه صبر کنید و دوباره بزنید."
	txtPaidConfirmed  = "پرداخت شما تأیید شد ✅"
)

// Delivery.
const (
	txtDelivered           = "🎉 اشتراک شما آماده است!\n\n📦 پلن: %s"
	txtDeliveredUntil      = "📅 اعتبار تا: %s"
	txtDeliverLink         = "🔗 لینک اشتراک شما:\n%s\n\nاین لینک را در برنامه وارد کنید. لینک مثل رمز شماست؛ آن را با کسی به اشتراک نگذارید."
	txtDeliverToken        = "🔑 کد اشتراک شما:\n<code>%s</code>\n\nفروشنده لینک کامل اشتراک را برایتان می‌فرستد."
	txtDeliverFailed       = "پرداخت شما تأیید شده، اما ساختن اشتراک به مشکلی خورد. نگران نباشید؛ به فروشنده خبر داده شد و اشتراکتان را دستی تحویل می‌دهد."
	txtDeliverFailedSeller = "⚠️ پرداخت سفارش %d تأیید شد ولی تحویل خودکار انجام نشد: %v\nخریدار: %s (<code>%d</code>)"
	txtSellerLinkNote      = "ℹ️ خریدار «%s» اشتراک خرید، اما لینک عمومی پنل تنظیم نیست و فقط کد اشتراک برایش رفت:\n<code>%s</code>\nلینک کامل را دستی برایش بفرستید."
)

// What a delivery says beyond the link depends on the section, because
// the two are opened in different apps: a WireGuard buyer is pointed at
// the apps that speak it, and a V2ray buyer is handed the bundle link
// their apps import directly, so neither has to know the other's way.
const (
	txtDeliverWireGuardGuide = "🛡 کانفیگ‌های وایرگاردتان داخل همین لینک اشتراک است؛ آن را با هیدیفای یا برنامهٔ وایرگارد باز کنید."
	txtDeliverV2rayLink      = "🚀 لینک مستقیم کانفیگ‌ها (برای V2Box و v2rayNG):\n%s"
)

// "Mine".
const (
	txtMineEmpty    = "شما هنوز اشتراکی ندارید. از دکمهٔ زیر می‌توانید اولین اشتراکتان را بخرید."
	txtMineTitle    = "📊 اشتراک شما"
	txtMineStatus   = "وضعیت: %s"
	txtMineUsed     = "مصرف: %s از %s"
	txtMineLeft     = "باقی‌مانده: %s"
	txtMineExpiry   = "انقضا: %s (%s)"
	txtMineNoExpiry = "انقضا: ندارد"
	txtMineLink     = "🔗 لینک اشتراک:\n%s"
	txtMineToken    = "🔑 کد اشتراک: <code>%s</code>"
)

// "Mine" names each section the customer holds with the link that
// section is used through -- the subscription page for WireGuard, the
// direct bundle for the V2ray apps -- rather than one bare link the
// buyer has to guess the app for.
const (
	txtMineWireGuard = "🛡 وایرگارد — لینک اشتراک:\n%s"
	txtMineV2ray     = "🚀 V2ray — لینک مستقیم کانفیگ‌ها (V2Box و v2rayNG):\n%s"
)

// Announcements. The prefix is on every one, so a message that arrives
// out of the blue is known for what it is; the refusals are the
// operator's sentences, which the panel shows them as written.
const txtAnnouncePrefix = "📣 پیام فروشنده:\n\n"

var (
	errAnnounceBotOff   = errors.New("بات فروش روشن نیست؛ اول از صفحه «بات فروش» پنل بات را روشن کنید")
	errAnnounceNoBuyers = errors.New("هنوز هیچ مشتری‌ای از بات خرید نکرده است")
)

// Support.
const (
	txtSupportAsk     = "پیام خود را بنویسید و بفرستید؛ هر متنی یا عکسی بفرستید به فروشنده می‌رسد."
	txtSupportSent    = "پیام شما برای فروشنده فرستاده شد ✅"
	txtSupportOff     = "پشتیبانی این فروشگاه فعال نیست."
	txtSupportFrom    = "💬 پیام پشتیبانی\nاز: %s (<code>%d</code>)\nبرای پاسخ، روی پیام بعدی ریپلای بزنید:"
	txtSupportReplied = "💬 پاسخ پشتیبانی:\n"
)

// statusText is a customer's state in the buyer's words.
func statusText(s model.ClientStatus) string {
	switch s {
	case model.StatusActive:
		return "فعال ✅"
	case model.StatusDisabled:
		return "غیرفعال"
	case model.StatusExpired:
		return "منقضی شده"
	case model.StatusExhausted:
		return "حجم تمام شده"
	}
	return string(s)
}

// faDigits rewrites Latin digits as Persian ones.
func faDigits(s string) string {
	return strings.NewReplacer(
		"0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴",
		"5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹",
	).Replace(s)
}

// faNum writes a whole number the way a Persian reader expects it:
// grouped in thousands, in Persian digits.
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

// formatPrice is a plan's price, in Toman, in words a buyer scans.
func formatPrice(toman int64) string {
	return faNum(toman) + " تومان"
}

// formatBytes is a volume in gigabytes, the unit plans are sold in.
// Zero is the panel's "no ceiling", and says so.
func formatBytes(b uint64) string {
	if b == 0 {
		return "نامحدود"
	}
	gb := float64(b) / (1 << 30)
	if gb == float64(uint64(gb)) {
		return faNum(int64(gb)) + " گیگابایت"
	}
	return faDigits(fmt.Sprintf("%.1f", gb)) + " گیگابایت"
}

// formatDays is a plan's length.
func formatDays(days int) string {
	if days <= 0 {
		return "بدون محدودیت زمانی"
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

// planButtonText is one plan on the buy menu: what it is, how much, how
// long, how much it costs — everything the buyer decides on, in the
// button itself.
func planButtonText(p *shop.Plan) string {
	return fmt.Sprintf("%s — %s — %s — %s",
		p.Name, formatBytes(p.QuotaBytes), formatDays(p.Days), formatPrice(p.PriceToman))
}

// buyerName is how an order names its buyer.
func buyerName(u *tgUser) string {
	if u == nil {
		return ""
	}
	return u.name()
}
