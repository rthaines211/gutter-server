# Gutter Server for Mac: UI test plan

Run this on a Mac before merging. It covers what the build checks can't: every
menu item, the Settings window, the folder picker, launch at login, and
recovery. Tick each box as you go; anything that fails, note the step number.

About 30 minutes for sections A to F, 10 more for G.

## Before you start

- A test comics folder with two or three series, one folder per series with
  `.cbz` files inside (for example `~/Documents/gutter-test-comics`).
- Your iPhone or iPad with Gutter, on the same Wi-Fi as the Mac.
- Terminal open for a few one-line checks.
- Open `macapp/GutterServer.xcodeproj` in Xcode, pick the **Gutter Server**
  scheme, and press Run (⌘R). The build should finish with no errors.

To start again from a first launch at any point (quit the app first):

```
rm -rf ~/Library/Containers/com.ryanhaines.gutter.server
security delete-generic-password -s com.ryanhaines.gutter.server
```

## A. First launch

- [ ] A1. A books icon (outline) appears in the menu bar. No Dock icon.
- [ ] A2. The menu reads "Gutter Server: Stopped" and "Choose a comics folder in Settings."
- [ ] A3. Start and Open Status Page are greyed out. Show Logs, Settings… and Quit work.
- [ ] A4. Settings… opens the window in front of other apps. Comics folder shows "None", Port 8081, Password empty.

## B. Choosing a folder and serving

- [ ] B1. Choose… opens a panel that says "Choose the folder that holds your comics." with a **Use Folder** button. Files can't be picked, folders can.
- [ ] B2. Pick the test folder. Its path shows in Settings (long paths cut in the middle).
- [ ] B3. The menu goes Starting… → Scanning (with "n of m files") → Running, and the icon fills in.
- [ ] B4. The Password field fills in by itself with a 16-character password (new in this update).
- [ ] B5. The menu lists "Copy server address" entries (your Wi-Fi IP, plus Tailscale if it's on). Click one and paste it into Notes: it reads `http://<ip>:8081`.
- [ ] B6. Open Status Page opens `http://127.0.0.1:8081/` in your browser. It asks you to log in; any user name with the password works and the page shows your series.
- [ ] B7. In Gutter on the iPad, add a server with the copied address and the password. The Library shows the test series and an issue opens and turns pages. If macOS asks whether to allow incoming connections, allow it and note that it asked.

## C. Menu actions

- [ ] C1. Stop: the menu reads Stopped, the icon goes back to outline, the address section disappears. `curl -s http://127.0.0.1:8081/healthz` fails.
- [ ] C2. Start: back to Running within a few seconds, and the iPad reconnects.
- [ ] C3. Show Logs opens `helper.log`. It has `[app]` lines such as "comics folder set to …" and "helper pid … on port 8081".
- [ ] C4. Quit Gutter Server (⌘Q in the menu): the icon disappears and `pgrep -fl gutter-server` prints nothing.
- [ ] C5. Launch the app again: it starts serving the same folder without asking.

## D. Settings

- [ ] D1. Type port `18081` but don't save. Open Status Page still opens the working server on 8081, and the menu stays Running (new in this update).
- [ ] D2. Save and Restart Server: the status page now opens on 18081, the copied addresses end in `:18081`, and `curl -s http://127.0.0.1:8081/healthz` fails.
- [ ] D3. Change the password and save. The old password gets a 401 on the status page, the new one works, and the menu stays Running.
- [ ] D4. Clear the password and press Save: a red "Set a port (1–65535) and a password." appears and nothing restarts. Same for port 0 and port 70000.
- [ ] D5. Status-page settings now survive a restart (new in this update): open `http://127.0.0.1:<port>/_admin/scanner`, change the scan schedule, then Save and Restart Server in the app. The schedule you picked is still set.
- [ ] D6. Set the port back to 8081 and save.

## E. Launch at login

- [ ] E1. Turn on Launch at login. System Settings → General → Login Items lists Gutter Server.
- [ ] E2. Log out and back in (or restart). The app is in the menu bar and the server is Running without you touching it.
- [ ] E3. Turn it off. It leaves Login Items, and after logging out and in the app doesn't start.

## F. Folder changes and recovery

- [ ] F1. With the app quit, rename the comics folder in Finder. Relaunch: it still serves the folder, and Settings shows the new name.
- [ ] F2. With the app quit, move the folder to the Trash and empty it. Relaunch: the menu says "The comics folder can't be found. Choose it again in Settings." Choosing a folder fixes it.
- [ ] F3. `open -a "Gutter Server" ~/Documents/gutter-test-comics` sets that folder and restarts the server.
- [ ] F4. Kill the server process: `pkill -9 -f "MacOS/gutter-server"`. The menu shows "Server exited (9); restarting in 2s. See logs." and goes back to Running.
- [ ] F5. Force quit the app in Activity Monitor. Within about 2 seconds `pgrep -fl gutter-server` prints nothing, and relaunching works on the same port.
- [ ] F6. Put the Mac to sleep for a minute and wake it. The iPad can still browse and read.

## G. Developer ID build and appearance

- [ ] G1. Switch the scheme to **Gutter Server (Developer ID)** and Run. Repeat A1 to B7. Its files are in `~/Library/Application Support/Gutter` instead of the container.
- [ ] G2. Switch macOS between Light and Dark. The menu and the Settings window read well in both.

## Known gaps (not bugs in this PR)

- No app icon yet; the App Store needs one before submission.
- Folders added on the status page's Config screen are replaced by the app's folder on the next start.
- Writeback into the comics folder is off, and chapter detection (OCR) doesn't run in the sandbox.
- Archive/export, notarization and a DMG are not covered here.
