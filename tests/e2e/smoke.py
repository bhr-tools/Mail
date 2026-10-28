"""End-to-end smoke test of the web app in Chromium (desktop and phone).

Run against a fresh instance (see tests/e2e/run.sh): one account with
sample mail, served from memory. Screenshots are written to $OUT.
"""
import os
import re
import sys

from playwright.sync_api import sync_playwright, expect

BASE = os.environ.get("BASE_URL", "http://localhost:18090")
OUT = os.environ.get("OUT", "/tmp")
PASSWORD = "correct horse"
errors = []


def watch(page):
    page.on("console", lambda m: m.type in ("error", "warning") and "401" not in m.text
            and errors.append(f"console {m.type}: {m.text}"))
    page.on("pageerror", lambda e: errors.append(f"page error: {e}"))


def shot(page, name):
    page.wait_for_timeout(300)
    page.screenshot(path=f"{OUT}/{name}.png")


def rows(page):
    return page.locator(".thread-row")


def row(page, subject):
    return page.locator(".thread-row", has_text=subject)


def frame_of(page, card):
    return card.locator("iframe.mail-frame").content_frame


def folders(page):
    return page.locator("#folders .folder-row")


def rect(locator):
    """Box of an element, measured in one go: the views may be drawn again
    meanwhile (bounding_box then gives None)."""
    return locator.evaluate("e => { const r = e.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height }; }")


def swipe(page, dx, lift=True):
    """Slides a finger across the first conversation, dx pixels sideways.
    Pointer events sent from the page: the touch input of CDP proved
    unreliable here (touches lost, or landing at 0,0), and the row is looked
    up as the events are sent, the list being drawn again at any time."""
    page.evaluate("""([dx, lift]) => {
        const row = document.querySelector('.thread-row');
        const r = row.getBoundingClientRect();
        const x = r.x + r.width / 2, y = r.y + r.height / 2;
        const fire = (type, cx) => row.dispatchEvent(new PointerEvent(type,
            { pointerId: 7, pointerType: 'touch', isPrimary: true, clientX: cx, clientY: y, bubbles: true }));
        fire('pointerdown', x);
        for (let i = 1; i <= 12; i++) fire('pointermove', x + dx * i / 12);
        if (lift) fire('pointerup', x + dx);
    }""", [dx, lift])


def setup(page):
    page.goto(BASE)
    page.get_by_placeholder("At least 4 characters").fill(PASSWORD)
    page.get_by_placeholder("Repeat the password").fill(PASSWORD)
    page.get_by_role("button", name="Set password and continue").click()
    expect(rows(page)).to_have_count(4)


def desktop(browser):
    ctx = browser.new_context(viewport={"width": 1366, "height": 860}, color_scheme="dark")
    page = ctx.new_page()
    watch(page)
    setup(page)
    expect(page.locator("#list-title")).to_contain_text("Inbox")
    # Folded: the inbox only. + unfolds every folder, the own ones included.
    expect(folders(page)).to_have_count(1)
    page.keyboard.press("+")
    expect(folders(page)).to_have_text(["Inbox4", "Archive", "Sent", "Drafts1", "Trash", "Projects", "Robotics1", "Receipts"])
    expect(page.locator(".nav-section")).to_have_attribute("aria-expanded", "true")
    expect(rows(page).first).to_contain_text("Climbing on Saturday?")
    expect(page.locator(".thread-row.unread")).to_have_count(4)
    expect(page.locator(".row-snippet").first).to_contain_text("climbing gym")
    shot(page, "01-inbox")

    # The reader always shows the conversation under the cursor.
    expect(page.locator(".reader-subject")).to_have_text("Climbing on Saturday?")
    # j: the newsletter, remote images blocked.
    page.keyboard.press("j")
    expect(rows(page).nth(1)).to_have_class(re.compile("cursor"))
    card = page.locator(".message-card").last
    expect(page.locator(".reader-subject")).to_have_text("Five books for the evenings")
    expect(card.locator(".images-banner")).to_be_visible()
    frame = frame_of(page, card)
    expect(frame.locator("h1")).to_have_text("Five books for the evenings")
    assert frame.locator("img").first.get_attribute("src") in ("", None), "remote image loaded without asking"
    assert frame.locator("script").count() == 0
    # Shown a moment, it is read; one passed over quickly stays unread.
    expect(row(page, "Five books")).not_to_have_class(re.compile("unread"))
    expect(row(page, "Climbing on Saturday?")).to_have_class(re.compile("unread"))
    # Escape does not close the reader.
    page.keyboard.press("Escape")
    expect(page.locator(".reader-subject")).to_have_text("Five books for the evenings")
    shot(page, "02-images-blocked")

    page.keyboard.press("i")
    expect(card.locator(".images-banner")).to_have_count(0)
    frame = frame_of(page, card)
    banner = frame.locator("img").first
    expect(banner).to_have_attribute("src", re.compile(r"^/api/img\?u="))
    page.wait_for_function("""() => {
        const f = document.querySelector('iframe.mail-frame');
        const img = f && f.contentDocument && f.contentDocument.images[0];
        return img && img.complete && img.naturalWidth > 0;
    }""")
    height = page.evaluate("document.querySelector('iframe.mail-frame').getBoundingClientRect().height")
    assert height > 250, f"frame not fitted to its content: {height}px"
    shot(page, "03-images-shown")

    # Keys pressed inside the frame still work.
    frame.locator("h1").click()
    page.keyboard.press("t")
    expect(card.locator(".mail-text")).to_contain_text("Five books for the evenings")
    page.keyboard.press("t")
    expect(card.locator("iframe.mail-frame")).to_be_visible()

    # d: done. The reader moves on to the next conversation; z undoes.
    page.keyboard.press("d")
    expect(page.locator(".toast")).to_contain_text("Archived")
    expect(rows(page)).to_have_count(3)
    expect(page.locator(".reader-subject")).to_have_text("Invoice for the gala hall")
    # Shift+J / Shift+K: next / previous folder, the reader open.
    page.keyboard.press("J")
    expect(page.locator("#list-title")).to_contain_text("Archive")
    expect(row(page, "Five books")).to_have_count(1)
    expect(page.locator(".reader-subject")).to_have_text(re.compile("."))
    page.keyboard.press("K")
    expect(page.locator("#list-title")).to_contain_text("Inbox")
    page.keyboard.press("z")
    expect(page.locator(".toast")).to_contain_text("Undone")
    expect(rows(page)).to_have_count(4)

    # Attachments open from the reader.
    row(page, "Invoice for the gala hall").click()
    attachment = page.locator(".attachment-name", has_text="invoice-gala.pdf")
    expect(attachment).to_be_visible()
    href = attachment.get_attribute("href")
    res = page.request.get(BASE + href)
    assert res.ok and res.headers["content-type"] == "application/pdf", res.headers

    # A conversation with a reply from Sent: every message unfolded.
    page.keyboard.press("G")
    expect(page.locator(".reader-subject")).to_have_text("Re: Lab report review")
    cards = page.locator(".message-card")
    expect(cards).to_have_count(3)
    expect(page.locator(".message-card.collapsed")).to_have_count(0)
    expect(cards.nth(1)).to_contain_text("Ben")
    expect(cards.nth(2)).not_to_have_class(re.compile("collapsed"))
    expect(cards.nth(2).locator(".mail-text")).to_contain_text("I added two figures")
    # Quotes are folded behind a toggle.
    expect(cards.nth(2).locator(".quote")).to_be_hidden()
    shot(page, "04-thread")

    # r: reply, typed at once (before the reply is prefilled), sent with Ctrl+Enter.
    page.keyboard.press("r")
    page.keyboard.type("Thanks, I will look at the figures.")
    compose = page.locator(".modal.compose")
    expect(compose).to_be_visible()
    expect(compose.get_by_label("Subject")).to_have_value("Re: Lab report review")
    expect(compose.locator(".compose-input").nth(0)).to_have_value(re.compile("camille@inp.test"))
    body = compose.get_by_label("Message")
    expect(body).to_be_focused()
    expect(body).to_have_value(re.compile(r"^Thanks, I will look at the figures\.\n\nOn .+, Camille Martin wrote:\n> Great"))
    shot(page, "05-reply")
    page.keyboard.press("Control+Enter")
    expect(page.locator(".toast")).to_contain_text("Message sent")
    expect(compose).to_have_count(0)

    # c: new message, address completed from the address book; a mentioned
    # attachment that is missing is caught; Escape keeps a draft.
    page.keyboard.press("c")
    to = compose.get_by_label("To")
    expect(to).to_be_focused()
    page.keyboard.type("lea")
    expect(compose.locator(".suggestion").first).to_contain_text("Léa Dubois")
    page.keyboard.press("Enter")
    expect(to).to_have_value("Léa Dubois <lea@friends.test>, ")
    compose.get_by_label("Subject").fill("Saturday")
    compose.get_by_label("Message").fill("Count me in! The route plan is attached.")
    page.keyboard.press("Control+Enter")
    expect(page.locator(".modal.dialog")).to_contain_text("mentions an attachment")
    page.keyboard.press("Escape")
    expect(compose).to_be_visible()
    page.keyboard.press("Escape")
    expect(page.locator(".toast")).to_contain_text("Draft saved")
    expect(compose).to_have_count(0)

    for _ in range(3):
        page.keyboard.press("J")
    expect(page.locator("#list-title")).to_contain_text("Drafts")
    expect(row(page, "Saturday")).to_have_count(1)
    expect(row(page, "Saturday")).to_contain_text("To: Léa Dubois")
    # Opening a draft finishes it in the composer.
    row(page, "Saturday").click()
    expect(compose.get_by_label("Subject")).to_have_value("Saturday")
    expect(compose.get_by_label("Message")).to_have_value(re.compile("Count me in"))
    page.keyboard.press("Escape")

    # The reply reached Sent.
    page.keyboard.press("K")
    expect(page.locator("#list-title")).to_contain_text("Sent")
    # Both answers to Camille, in one conversation.
    expect(rows(page)).to_have_count(1)
    expect(row(page, "Re: Lab report review").locator(".row-count")).to_have_text("2")
    expect(row(page, "Re: Lab report review")).to_contain_text("To: Camille Martin")

    # Search, with a filter.
    page.keyboard.press("/")
    page.keyboard.type("invoice")
    page.keyboard.press("Enter")
    expect(page.locator("#list-title")).to_contain_text("invoice")
    expect(rows(page)).to_have_count(1)
    page.keyboard.press("/")
    page.keyboard.press("Control+a")
    page.keyboard.type("from:camille is:unread")
    page.keyboard.press("Enter")
    expect(rows(page)).to_have_count(0)
    page.keyboard.press("Escape")
    expect(page.locator("#list-title")).to_contain_text("Sent")

    # A folded account still shows the folder being viewed.
    page.keyboard.press("+")
    expect(folders(page)).to_have_text([re.compile(r"^Inbox\d*$"), "Sent"])
    page.keyboard.press("+")

    # An own folder (a Gmail label), nested.
    for _ in range(4):
        page.keyboard.press("J")
    expect(page.locator("#list-title")).to_contain_text("Robotics")
    expect(rows(page)).to_have_count(1)
    expect(rows(page).first).to_contain_text("Motor driver datasheet")

    # Star and read state, in the inbox.
    for _ in range(6):
        page.keyboard.press("K")
    expect(page.locator("#list-title")).to_contain_text("Inbox")
    expect(rows(page)).to_have_count(4)
    page.keyboard.press("j")
    page.keyboard.press("s")
    expect(rows(page).nth(1).locator(".icon-star")).to_have_count(1)
    page.keyboard.press("u")
    expect(rows(page).nth(1)).to_have_class(re.compile("unread"))
    page.keyboard.press("u")
    expect(rows(page).nth(1)).not_to_have_class(re.compile("unread"))

    # m: move to a folder picked by typing.
    page.keyboard.press("m")
    page.keyboard.type("recei")
    expect(page.locator(".picker-item")).to_have_count(1)
    page.keyboard.press("Enter")
    expect(page.locator(".toast")).to_contain_text("Moved to Receipts")
    expect(rows(page)).to_have_count(3)

    page.keyboard.press("?")
    expect(page.locator(".modal.help")).to_be_visible()
    shot(page, "06-help")
    page.keyboard.press("Escape")

    page.emulate_media(color_scheme="light")
    expect(page.locator(".reader-subject")).to_be_visible()
    shot(page, "07-light")
    ctx.close()


def phone(browser):
    ctx = browser.new_context(viewport={"width": 390, "height": 844}, device_scale_factor=2,
                              is_mobile=True, has_touch=True, color_scheme="dark")
    page = ctx.new_page()
    watch(page)
    page.goto(BASE)
    page.get_by_placeholder("Master password").fill(PASSWORD)
    page.get_by_role("button", name="Unlock").click()
    expect(rows(page).first).to_be_visible()
    shot(page, "08-phone-list")
    count = rows(page).count()
    rows(page).first.tap()
    expect(page.locator(".reader-title")).to_be_visible()
    expect(page.locator(".reader-subject")).to_be_hidden()
    expect(page.locator(".list-pane")).to_be_hidden()
    shot(page, "09-phone-reader")
    page.locator(".back-button").tap()
    expect(page.locator(".list-pane")).to_be_visible()

    # Swipes: right to move, left to the trash (z brings it back).
    swipe(page, -200, lift=False)
    expect(page.locator(".swipe.armed .swipe-left")).to_be_visible()
    shot(page, "08b-phone-swipe")
    page.evaluate("document.querySelector('.swipe.swiping .thread-row').dispatchEvent(new PointerEvent('pointercancel', { pointerId: 7, pointerType: 'touch', bubbles: true }))")
    expect(page.locator(".swipe.to-left")).to_have_count(0)
    swipe(page, 250)
    expect(page.locator(".modal.picker")).to_be_visible()
    page.keyboard.press("Escape")
    expect(rows(page)).to_have_count(count)
    swipe(page, 60)
    expect(page.locator(".swipe.to-right")).to_have_count(0)
    expect(page.locator(".modal.picker")).to_have_count(0)
    swipe(page, -250)
    expect(page.locator(".toast")).to_contain_text("Moved to the trash")
    expect(rows(page)).to_have_count(count - 1)
    expect(page.locator(".reader-title")).to_be_hidden()
    page.keyboard.press("z")
    expect(rows(page)).to_have_count(count)

    # Mail laid out for a desktop: a long subject that wraps, a 600 pixel
    # table scaled down to the width of the phone.
    page.keyboard.press("+")
    page.keyboard.press("J")
    expect(page.locator("#list-title")).to_contain_text("Archive")
    row(page, "e-learning").tap()
    title = page.locator(".reader-title")
    expect(title).to_contain_text("e-learning module on phishing")
    header = rect(page.locator(".reader-header"))
    assert header["height"] < 80, f"reader header grew: {header}"
    assert rect(title)["y"] >= header["y"] + header["height"] - 1, "subject under the header"
    page.wait_for_function("""() => {
        const f = document.querySelector('iframe.mail-frame');
        const d = f && f.contentDocument;
        return d && d.body && d.body.style.zoom && d.documentElement.scrollWidth <= f.clientWidth + 1;
    }""")
    fit = page.evaluate("""() => {
        const f = document.querySelector('iframe.mail-frame');
        return { zoom: Number(f.contentDocument.body.style.zoom), frame: f.clientWidth, height: f.clientHeight };
    }""")
    assert 0.4 < fit["zoom"] < 0.8 and fit["height"] > 150, f"wide mail not fitted: {fit}"
    shot(page, "09b-phone-wide-mail")
    page.locator(".more-actions").tap()
    expect(page.locator(".popover.sheet")).to_contain_text("Mark as unread")
    page.keyboard.press("Escape")
    page.locator(".back-button").tap()
    page.keyboard.press("K")
    expect(page.locator("#list-title")).to_contain_text("Inbox")
    page.locator("#menu-toggle").tap()
    expect(page.locator(".sidebar.open")).to_be_visible()
    shot(page, "10-phone-folders")
    ctx.close()


with sync_playwright() as p:
    browser = p.chromium.launch()
    try:
        desktop(browser)
        phone(browser)
    finally:
        browser.close()

if errors:
    print("\n".join(errors))
    sys.exit(1)
print("e2e ok")
