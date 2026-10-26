"""End-to-end smoke test of the web app in Chromium (desktop and phone).

Run against a fresh demo instance (see tests/e2e/run.sh): one account with
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
    expect(rows(page).first).to_contain_text("Climbing on Saturday?")
    expect(page.locator(".thread-row.unread")).to_have_count(4)
    expect(page.locator(".row-snippet").first).to_contain_text("climbing gym")
    shot(page, "01-inbox")

    # j l: the newsletter opens, remote images blocked.
    page.keyboard.press("j")
    expect(rows(page).nth(1)).to_have_class(re.compile("cursor"))
    page.keyboard.press("l")
    card = page.locator(".message-card").last
    expect(page.locator(".reader-subject")).to_have_text("Five books for the evenings")
    expect(card.locator(".images-banner")).to_be_visible()
    frame = frame_of(page, card)
    expect(frame.locator("h1")).to_have_text("Five books for the evenings")
    assert frame.locator("img").first.get_attribute("src") in ("", None), "remote image loaded without asking"
    assert frame.locator("script").count() == 0
    # Opening read it.
    expect(row(page, "Five books")).not_to_have_class(re.compile("unread"))
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
    page.keyboard.press("g")
    page.keyboard.press("a")
    expect(row(page, "Five books")).to_have_count(1)
    page.keyboard.press("g")
    page.keyboard.press("i")
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

    # A conversation with a reply from Sent: unread message unfolded.
    page.keyboard.press("G")
    expect(page.locator(".reader-subject")).to_have_text("Re: Lab report review")
    cards = page.locator(".message-card")
    expect(cards).to_have_count(3)
    expect(cards.nth(0)).to_have_class(re.compile("collapsed"))
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

    page.keyboard.press("g")
    page.keyboard.press("d")
    expect(page.locator("#list-title")).to_contain_text("Drafts")
    expect(row(page, "Saturday")).to_have_count(1)
    expect(row(page, "Saturday")).to_contain_text("To: Léa Dubois")
    # Opening a draft finishes it in the composer.
    row(page, "Saturday").click()
    expect(compose.get_by_label("Subject")).to_have_value("Saturday")
    expect(compose.get_by_label("Message")).to_have_value(re.compile("Count me in"))
    page.keyboard.press("Escape")

    # The reply reached Sent.
    page.keyboard.press("g")
    page.keyboard.press("s")
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

    # Star and read state, in the inbox.
    page.keyboard.press("g")
    page.keyboard.press("i")
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
    page.keyboard.press("l")
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
    rows(page).first.tap()
    expect(page.locator(".reader-subject")).to_be_visible()
    expect(page.locator(".list-pane")).to_be_hidden()
    shot(page, "09-phone-reader")
    page.locator(".back-button").tap()
    expect(page.locator(".list-pane")).to_be_visible()
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
