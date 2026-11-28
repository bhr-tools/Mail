"""End-to-end smoke test of the web app in Chromium (desktop and phone).

Run against a fresh instance (see tests/e2e/run.sh): one account with
sample mail, served from memory. Screenshots are written to $OUT.
"""
import os
import datetime
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
    return page.locator("#thread-list .thread-row")


def row(page, subject):
    return page.locator("#thread-list .thread-row", has_text=subject)


def frame_of(page, card):
    return card.locator("iframe.mail-frame").content_frame


def folders(page):
    return page.locator("#folders .folder-row:not(.new-folder)")


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
    expect(folders(page)).to_have_text(["Inbox4", "Archive", "Sent", "Drafts1", "Trash", "Spam", "Projects", "Robotics1", "Receipts"])
    expect(page.locator(".nav-section")).to_have_attribute("aria-expanded", "true")
    expect(rows(page).first).to_contain_text("Climbing on Saturday?")
    expect(page.locator(".thread-row.unread")).to_have_count(4)
    expect(page.locator(".row-snippet").first).to_contain_text("climbing gym")
    shot(page, "01-inbox")
    # A window shorter than the folders: the sidebar scrolls, nothing in it shrinks.
    page.set_viewport_size({"width": 1366, "height": 300})
    assert rect(page.locator("#compose-button"))["height"] == 36, rect(page.locator("#compose-button"))
    assert page.locator("#sidebar").evaluate("e => e.scrollHeight > e.clientHeight")
    page.set_viewport_size({"width": 1366, "height": 860})

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
    # Redrawing the reader keeps the frame: a reload makes the mail jump.
    page.evaluate("document.querySelector('iframe.mail-frame').contentWindow.kept = true")
    page.keyboard.press("s")
    expect(page.locator(".reader-actions .icon-button.on")).to_have_count(1)
    page.keyboard.press("s")
    expect(page.locator(".reader-actions .icon-button.on")).to_have_count(0)
    page.wait_for_timeout(300)
    assert page.evaluate("document.querySelector('iframe.mail-frame').contentWindow.kept === true"), "frame reloaded on redraw"
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

    # Shift+A: done. The reader moves on to the next conversation; u undoes.
    page.keyboard.press("A")
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
    page.keyboard.press("u")
    expect(page.locator(".toast")).to_contain_text("Undone")
    expect(rows(page)).to_have_count(4)

    # Space selects and moves on to the next; d trashes the selection.
    page.keyboard.press("g")
    page.keyboard.press("g")
    page.keyboard.press(" ")
    page.keyboard.press(" ")
    expect(page.locator(".thread-row.selected")).to_have_count(2)
    expect(rows(page).nth(2)).to_have_class(re.compile("cursor"))
    page.keyboard.press("d")
    expect(page.locator(".toast")).to_contain_text("Moved to the trash · 2 conversations")
    expect(rows(page)).to_have_count(2)
    page.keyboard.press("u")
    expect(rows(page)).to_have_count(4)

    # u goes back several actions, latest first.
    page.keyboard.press("g")
    page.keyboard.press("g")
    page.keyboard.press("d")
    expect(rows(page)).to_have_count(3)
    page.keyboard.press("d")
    expect(rows(page)).to_have_count(2)
    # Pressed at once: the second trash is undone, not the action before.
    page.keyboard.press("u")
    expect(rows(page)).to_have_count(3)
    page.keyboard.press("u")
    expect(rows(page)).to_have_count(4)
    # The star and its removal, further back, are still there.
    expect(page.locator(".toast")).to_contain_text("Undone: moved to the trash")

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
    # Newest first.
    expect(cards.nth(1)).to_contain_text("Ben")
    expect(cards.nth(0)).not_to_have_class(re.compile("collapsed"))
    expect(cards.nth(0).locator(".mail-text")).to_contain_text("I added two figures")
    # Quotes are folded behind a toggle.
    expect(cards.nth(0).locator(".quote")).to_be_hidden()
    # Unfolded, the addresses are shown in full, and selecting them does not
    # fold the message.
    who = cards.nth(0).locator(".message-from strong")
    expect(cards.nth(0).locator(".message-to")).to_contain_text("@")
    who.dblclick(position={"x": 8, "y": 6})
    assert page.evaluate("String(getSelection())").strip(), "nothing selected"
    expect(cards.nth(0)).not_to_have_class(re.compile("collapsed"))
    cards.nth(0).locator(".message-meta").click()
    expect(cards.nth(0)).to_have_class(re.compile("collapsed"))
    cards.nth(0).locator(".message-head").click()
    expect(cards.nth(0)).not_to_have_class(re.compile("collapsed"))
    shot(page, "04-thread")

    # r: reply, typed at once (before the reply is prefilled), sent with Ctrl+Enter.
    page.keyboard.press("r")
    page.keyboard.type("Thanks, I will look at the figures.")
    compose = page.locator(".modal.compose")
    expect(compose).to_be_visible()
    expect(compose.get_by_label("Subject")).to_have_value("Re: Lab report review")
    expect(compose.get_by_label("Alias")).to_be_hidden()
    expect(compose.get_by_label("To")).to_have_value(re.compile("camille@inp.test"))
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

    # Search: a page of its own, results as you type.
    search = page.locator(".search-page")
    hits = page.locator(".search-hit")
    folder = page.locator(".search-select")
    filters = page.locator(".filters-page")
    open_filters = lambda: page.locator(".filters-button").click()
    close_filters = lambda: filters.locator(".modal-footer .primary").click()
    page.keyboard.press("/")
    expect(search).to_be_visible()
    page.keyboard.type("invoice")
    expect(hits).to_have_count(1)
    expect(hits.first).to_contain_text("Invoice for the gala hall")
    shot(page, "search")
    # The filters are on a page of their own; results update behind it.
    open_filters()
    expect(filters).to_be_visible()
    # The folder filter: the invoice is in the inbox, not the archive.
    folder.select_option("archive")
    expect(filters.locator(".modal-footer .primary")).to_have_text("Show 0 conversations")
    close_filters()
    expect(filters).to_have_count(0)
    expect(hits).to_have_count(0)
    expect(search).to_contain_text("No conversation matches.")
    # The active filter is a chip under the field; a click drops it.
    expect(page.locator(".filters-button .filter-badge")).to_have_text("1")
    page.locator(".search-active .search-chip", has_text="In Archive").click()
    expect(hits).to_have_count(1)
    expect(page.locator(".search-active .search-chip")).to_have_count(0)
    # Ctrl+Enter: the results in the list, to act on them.
    page.keyboard.press("Control+Enter")
    expect(search).to_have_count(0)
    expect(page.locator("#list-title")).to_contain_text("invoice")
    expect(rows(page)).to_have_count(1)
    # The period, then Enter opens a result.
    page.keyboard.press("/")
    expect(page.locator("#search-input")).to_have_value("invoice")
    page.locator("#search-input").fill("kickoff")
    expect(hits).to_have_count(1)
    open_filters()
    filters.get_by_role("button", name="30 days").click()
    expect(hits).to_have_count(0)
    filters.get_by_role("button", name="Any time").click()
    expect(hits).to_have_count(1)
    # Precise dates: the kickoff is 40 days old; one day, then a range.
    day = lambda n: (datetime.date.today() - datetime.timedelta(days=n)).isoformat()
    filters.get_by_role("button", name="Dates").click()
    expect(filters.locator(".search-dates")).to_contain_text("Pick a day")
    filters.get_by_label("Day, or first day").fill(day(40))
    expect(hits).to_have_count(1)
    expect(filters.locator(".search-dates")).to_contain_text("Just this day")
    filters.get_by_label("Day, or first day").fill(day(39))
    expect(hits).to_have_count(0)
    filters.get_by_label("Day, or first day").fill(day(45))
    filters.get_by_label("Last day (optional)").fill(day(35))
    expect(hits).to_have_count(1)
    shot(page, "search-dates")
    filters.get_by_role("button", name="Any time").click()
    expect(filters.locator(".search-dates")).to_be_hidden()
    expect(hits).to_have_count(1)
    # Escape leaves the filters for the results.
    page.keyboard.press("Escape")
    expect(filters).to_have_count(0)
    expect(search).to_be_visible()
    page.locator("#search-input").focus()
    page.keyboard.press("ArrowDown")
    expect(hits.first).to_have_class(re.compile("cursor"))
    page.keyboard.press("Enter")
    expect(search).to_have_count(0)
    expect(page.locator(".reader-subject")).to_have_text("Project kickoff")
    expect(page.locator("#list-title")).to_contain_text("kickoff")
    # Typed filters still work, and fill the form when it opens again.
    page.keyboard.press("/")
    page.locator("#search-input").fill("from:camille is:unread")
    expect(search).to_contain_text("No conversation matches.")
    page.keyboard.press("Escape")
    expect(search).to_have_count(0)
    page.keyboard.press("/")
    expect(page.locator("#search-input")).to_have_value("")
    expect(page.locator(".search-active .search-chip")).to_have_text(["Unread", "From camille"])
    open_filters()
    expect(page.locator(".search-person").first).to_have_value("camille")
    expect(filters.locator(".search-chip.on")).to_have_text(["Unread"])
    page.keyboard.press("Escape")
    page.keyboard.press("Escape")
    # Escape leaves the results for the folder.
    page.keyboard.press("Escape")
    expect(page.locator("#list-title")).to_contain_text("Sent")

    # A folded account still shows the folder being viewed.
    page.keyboard.press("+")
    expect(folders(page)).to_have_text([re.compile(r"^Inbox\d*$"), "Sent"])
    page.keyboard.press("+")

    # Spam, after the trash: I takes a wrongly caught mail back to the inbox.
    for _ in range(3):
        page.keyboard.press("J")
    expect(page.locator("#list-title")).to_contain_text("Spam")
    expect(rows(page)).to_have_count(1)
    expect(rows(page).first).to_contain_text("You won!")
    page.keyboard.press("I")
    expect(page.locator(".toast")).to_contain_text("Moved to the inbox")
    expect(rows(page)).to_have_count(0)
    expect(page.locator("#thread-list")).to_contain_text("No spam.")
    page.keyboard.press("u")
    expect(rows(page)).to_have_count(1)

    # An own folder (a Gmail label), nested.
    for _ in range(2):
        page.keyboard.press("J")
    expect(page.locator("#list-title")).to_contain_text("Robotics")
    expect(rows(page)).to_have_count(1)
    expect(rows(page).first).to_contain_text("Motor driver datasheet")

    # Star and read state, in the inbox.
    for _ in range(7):
        page.keyboard.press("K")
    expect(page.locator("#list-title")).to_contain_text("Inbox")
    expect(rows(page)).to_have_count(4)
    page.keyboard.press("j")
    page.keyboard.press("s")
    expect(rows(page).nth(1).locator(".icon-star")).to_have_count(1)
    page.keyboard.press("U")
    expect(rows(page).nth(1)).to_have_class(re.compile("unread"))
    page.keyboard.press("U")
    expect(rows(page).nth(1)).not_to_have_class(re.compile("unread"))

    # m: move to a folder picked by typing.
    page.keyboard.press("m")
    page.keyboard.type("recei")
    # The matching folder first; making one of that name, last.
    expect(page.locator(".picker-item")).to_have_text(["Receipts", "New folder “recei”"])
    page.keyboard.press("Enter")
    expect(page.locator(".toast")).to_contain_text("Moved to Receipts")
    expect(rows(page)).to_have_count(3)

    # m, then a name no folder has: the folder is made, the mail moved in.
    page.keyboard.press("m")
    page.keyboard.type("Travel")
    expect(page.locator(".picker-item.create")).to_have_text("New folder “Travel”")
    page.keyboard.press("Enter")
    expect(page.locator(".toast")).to_contain_text("Moved to Travel")
    expect(rows(page)).to_have_count(2)
    expect(folders(page).filter(has_text="Travel")).to_have_count(1)

    # Right click: the app's menu, not the browser's.
    rows(page).first.click(button="right")
    menu = page.locator(".popover .menu")
    expect(menu).to_contain_text("Archive")
    expect(menu).to_contain_text("Move to…")
    shot(page, "06a-context-menu")
    page.keyboard.press("Escape")
    expect(menu).to_have_count(0)

    # Dragged onto a folder of the sidebar, a conversation moves there; u
    # brings it back. A plain click still opens it.
    first = rows(page).first
    subject = first.locator(".row-subject-text").inner_text()
    count = rows(page).count()
    a, b = rect(first), rect(folders(page).filter(has_text="Receipts"))
    page.mouse.move(a["x"] + 60, a["y"] + a["height"] / 2)
    page.mouse.down()
    page.mouse.move(a["x"] + 40, a["y"] + 40, steps=4)
    expect(page.locator(".drag-ghost")).to_have_text("1 conversation")
    expect(page.locator(".thread-row.dragging")).to_have_count(1)
    page.mouse.move(b["x"] + 30, b["y"] + b["height"] / 2, steps=8)
    expect(folders(page).filter(has_text="Receipts")).to_have_class(re.compile("drop-target"))
    page.wait_for_timeout(300)
    assert page.locator(".thread-row.dragging").count() == 1, "the dragged row was drawn again"
    shot(page, "06b-drag-to-folder")
    page.mouse.up()
    expect(page.locator(".toast")).to_contain_text("Moved to Receipts")
    expect(rows(page)).to_have_count(count - 1)
    page.keyboard.press("u")
    expect(rows(page)).to_have_count(count)
    expect(row(page, subject)).to_have_count(1)
    rows(page).nth(1).click()
    expect(rows(page).nth(1)).to_have_class(re.compile("cursor"))

    # Folders: renamed and deleted from their own menu, made from the sidebar.
    folders(page).filter(has_text="Travel").click(button="right")
    page.locator(".menu-item", has_text="Rename").click()
    # The name is selected: typed at once, it replaces it whole.
    page.keyboard.type("Trips")
    page.keyboard.press("Enter")
    expect(folders(page).filter(has_text="Trips")).to_have_count(1)
    expect(folders(page).filter(has_text="Travel")).to_have_count(0)
    page.locator(".folder-row.new-folder").click()
    page.keyboard.type("Empty")
    page.keyboard.press("Enter")
    expect(folders(page).filter(has_text="Empty")).to_have_count(1)
    folders(page).filter(has_text="Empty").click(button="right")
    page.locator(".menu-item", has_text="Delete").click()
    page.get_by_role("button", name="Delete").click()
    expect(page.locator(".toast")).to_contain_text("Folder Empty deleted")
    expect(folders(page).filter(has_text="Empty")).to_have_count(0)
    page.locator(".nav-section").click(button="right")
    expect(page.locator(".popover .menu")).to_contain_text("New folder")
    page.keyboard.press("Escape")

    # addy.io: the account sends only from an alias; the draft keeps the real
    # address and the alias.
    page.locator("#settings-menu").click()
    page.locator(".menu-item", has_text="addy.io aliases").click()
    settings = page.locator(".modal.dialog")
    settings.get_by_label("Alias domain").fill("me.anonaddy.test")
    settings.get_by_role("checkbox").check()
    shot(page, "06-addy")
    settings.get_by_role("button", name="Save").click()
    expect(page.locator(".toast")).to_contain_text("addy.io settings saved")
    page.keyboard.press("c")
    compose = page.locator(".modal.compose")
    # Never from the account's own address: Send asks for an alias.
    compose.get_by_label("To").fill("bob@example.test")
    compose.get_by_label("Subject").fill("Order 42")
    compose.get_by_role("button", name="Send").click()
    expect(page.locator(".toast", has_text="Choose an alias")).to_be_visible()
    expect(compose.get_by_label("Alias")).to_be_focused()
    shot(page, "06-addy-required")
    compose.get_by_label("Alias").fill("shop")
    expect(compose.locator(".compose-suffix")).to_have_text("@me.anonaddy.test")
    compose.get_by_role("button", name="Save draft").click()
    expect(page.locator(".toast")).to_contain_text("Draft saved")
    page.locator(".folder-row", has_text="Drafts").click()
    row(page, "Order 42").click()
    expect(compose.get_by_label("Alias")).to_have_value("shop")
    expect(compose.get_by_label("To")).to_have_value("bob@example.test")
    page.keyboard.press("Escape")

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
    # Tabs at the bottom: Inbox (with its unread count), Folders, Search.
    expect(page.locator(".tab.active")).to_have_text(re.compile("Inbox"))
    expect(page.locator("#search-button")).to_be_hidden()
    # The search page fills the screen; nothing spills sideways.
    page.locator(".tab[data-tab=search]").tap()
    page.locator("#search-input").fill("lab")
    expect(page.locator(".search-hit")).to_have_count(1)
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth"), "search page wider than the phone"
    shot(page, "08c-phone-search")
    page.locator(".filters-button").tap()
    page.locator(".filters-page").get_by_role("button", name="Dates").tap()
    page.locator(".filters-page").get_by_label("Day, or first day").fill("2026-03-02")
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth"), "dates wider than the phone"
    shot(page, "08d-phone-search-dates")
    page.locator(".filters-page").get_by_role("button", name="Any time").tap()
    page.locator(".filters-page .modal-footer .primary").tap()
    expect(page.locator(".filters-page")).to_have_count(0)
    page.locator(".search-header .icon-button").tap()
    expect(page.locator(".search-page")).to_have_count(0)
    count = rows(page).count()
    rows(page).first.tap()
    expect(page.locator(".reader-title")).to_be_visible()
    expect(page.locator(".reader-subject")).to_be_hidden()
    expect(page.locator(".list-pane")).to_be_hidden()
    shot(page, "09-phone-reader")
    expect(page.locator(".back-button")).to_contain_text("Inbox")
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
    page.keyboard.press("u")
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
    # Mail through an alias forwarder: a long sender name, long addresses.
    # Nothing scrolls sideways, and the thumb swipe from the left edge goes
    # back even over the frame of the message (real touches, hit-tested).
    row(page, "one million").tap()
    expect(page.locator(".images-banner")).to_be_visible()
    page.wait_for_function("() => document.querySelector('iframe.mail-frame')?.contentDocument?.body")
    width = page.evaluate("() => { const r = document.querySelector('.reader-pane'); return [r.scrollWidth, r.clientWidth]; }")
    assert width[0] <= width[1], f"reader scrolls sideways: {width}"
    shot(page, "09c-phone-forwarded-mail")
    frame = rect(page.locator("iframe.mail-frame"))
    y = frame["y"] + min(frame["height"], 300) / 2
    cdp = ctx.new_cdp_session(page)
    touch = lambda kind, x=None: cdp.send("Input.dispatchTouchEvent", {"type": kind, "touchPoints": [] if x is None else [{"x": x, "y": y}]})
    touch("touchStart", 6)
    for x in range(20, 300, 20):
        touch("touchMove", x)
        page.wait_for_timeout(16)
    touch("touchEnd")
    expect(page.locator(".list-pane")).to_be_visible()
    # A folder is a page pushed from the Folders page: "‹ Folders" or a
    # swipe from the left edge goes back to it; every folder is listed.
    expect(page.locator("#menu-toggle")).to_contain_text("Folders")
    page.locator("#menu-toggle").tap()
    expect(page.locator(".sidebar")).to_be_visible()
    expect(page.locator(".tab.active")).to_have_text(re.compile("Folders"))
    expect(page.locator("#folders .folder-row", has_text="Robotics")).to_be_visible()
    expect(page.locator(".fab")).to_be_hidden()
    shot(page, "10-phone-folders")
    page.locator("#folders .folder-row", has_text="Sent").tap()
    expect(page.locator("#list-title")).to_contain_text("Sent")
    expect(page.locator(".sidebar")).to_be_hidden()
    page.locator(".tab[data-tab=inbox]").tap()
    expect(page.locator("#list-title")).to_contain_text("Inbox")
    expect(page.locator("#menu-toggle")).to_be_hidden()
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
