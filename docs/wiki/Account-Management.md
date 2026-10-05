# Account Management Guide

[中文](账号管理.zh-CN) | English

## Add a Xianyu account

1. Open Account Management and click Add New Account by QR Code.
2. Scan with the target Xianyu account and follow the page for login or security verification.
3. Wait for the account to appear, then add a note so multiple accounts are easy to distinguish.
4. Confirm the account is enabled before syncing products or configuring automation.

For face or risk-control verification, use only the official Xianyu app or the official flow shown by the page. Do not forward verification URLs. If only the account ID is shown, click Refresh Nickname and Avatar; scan again if refresh fails.

## Daily account operations

- **Enable/disable:** a disabled account does not process backend tasks; tasks resume when it is enabled.
- **Edit:** change notes, automatic delivery/shipping confirmation, processing pause duration, Cookie, and long-login settings.
- **Reauthorize by QR:** use after Cookie expiry, account disconnection, or failed profile refresh. QR login is the current production login path.
- **AI Settings:** enable AI for this account and set bargaining limits. The system-wide API and model are configured in System and AI.
- **Automatic Reviews and Daily Refresh:** open it from the account card, save settings, or click Review Now/Refresh Now for one run.
- **Delete:** delete only after confirming that related products, rules, and orders no longer need processing.

## Pause and automatic shipping confirmation

In Edit Account, Processing Pause Duration (minutes) temporarily stops order handling and resumes it when the duration expires. Use Pause Again Now to restart the pause using the current duration.

Automatic Shipping Confirmation only marks the Xianyu order as shipped; it does not prove that a card was sent. Complete automatic delivery still requires the product, automation rule, and inventory to be configured correctly.

## Bind notification channels

1. Create and test a channel in Notification Settings.
2. Return to Account Management and edit the target account.
3. Select channels under Notification Channel Binding and save.
4. To receive delivery, price-change, or review-gift results, select the corresponding Paid Delivery, Unpaid Price Change, or Review Gift event in the channel’s event filters.

Account alerts and transaction-automation notices follow the account binding. Creating a channel without binding it to an account produces no notices for that account.

## Login information

Login Cookies are encrypted by the system. After updating the Cookie or long-login setting, confirm that the account is online before re-enabling automation. Never send Cookies, passwords, or QR screenshots through the Wiki, tickets, or chat.
