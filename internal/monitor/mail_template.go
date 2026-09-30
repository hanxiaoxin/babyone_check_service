package monitor

import (
	"bytes"
	"html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
)

var emailTemplate = template.Must(template.New("notification").Parse(`<!doctype html><html lang="zh-CN"><body style="margin:0;background:#f3f6fb;font-family:Arial,sans-serif;color:#23324a"><table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td style="padding:28px 12px"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px;margin:auto;background:#fff;border:1px solid #e2e8f0;border-radius:14px"><tr><td style="padding:22px 26px;background:#172b4d;color:#fff;font-size:18px;font-weight:bold">◈ Babyone Check</td></tr><tr><td style="padding:24px 26px"><p style="color:{{.Color}};font-size:12px;font-weight:bold;margin:0 0 10px">{{.Label}}</p><h1 style="font-size:22px;line-height:1.4;margin:0 0 20px">{{.Subject}}</h1><table width="100%" cellpadding="0" cellspacing="0">{{range .Rows}}<tr><td style="padding:10px 0;border-bottom:1px solid #edf1f6;color:#75839a;width:115px;vertical-align:top">{{.Key}}</td><td style="padding:10px 0 10px 12px;border-bottom:1px solid #edf1f6;word-break:break-all;white-space:pre-wrap">{{.Value}}</td></tr>{{end}}</table><p style="font-size:12px;line-height:1.7;color:#8794a7;margin:22px 0 0">自动监测通知 · 请打开监测面板查看检测记录和最新状态。邮件发送时间不代表故障开始时间。</p></td></tr></table></td></tr></table></body></html>`))

type emailRow struct{ Key, Value string }

func buildEmail(c SMTPConfig, subject, body string) ([]byte, error) {
	data := struct {
		Subject, Label, Color string
		Rows                  []emailRow
	}{Subject: subject, Label: "监测通知", Color: "#365eea"}
	switch {
	case strings.Contains(subject, "down") || strings.Contains(subject, "异常"):
		data.Label = "服务异常"
		data.Color = "#d54d50"
	case strings.Contains(subject, "healthy") || strings.Contains(subject, "恢复"):
		data.Label = "服务恢复"
		data.Color = "#258758"
	case strings.Contains(subject, "expiry") || strings.Contains(subject, "到期"):
		data.Label = "证书到期预警"
		data.Color = "#be7b28"
	case strings.Contains(subject, "测试"):
		data.Label = "测试邮件"
	}
	keys := map[string]string{"Target": "服务名称", "Description": "项目描述", "Remaining": "剩余有效期", "Address": "检测地址", "State": "检测状态", "Checked at": "检测时间（UTC）", "HTTP status": "HTTP 状态", "Latency": "检测耗时", "Error": "异常详情", "Certificate expires": "证书到期（UTC）", "Sent at": "发送时间（UTC）"}
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ": ", 2)
		row := emailRow{Value: line}
		if len(parts) == 2 {
			row.Key = parts[0]
			row.Value = parts[1]
			if label, ok := keys[row.Key]; ok {
				row.Key = label
			}
		}
		data.Rows = append(data.Rows, row)
	}
	var html bytes.Buffer
	if err := emailTemplate.Execute(&html, data); err != nil {
		return nil, err
	}
	var content bytes.Buffer
	parts := multipart.NewWriter(&content)
	for _, part := range []struct{ kind, body string }{{"text/plain", body}, {"text/html", html.String()}} {
		header := textproto.MIMEHeader{"Content-Type": {part.kind + "; charset=UTF-8"}, "Content-Transfer-Encoding": {"quoted-printable"}}
		writer, err := parts.CreatePart(header)
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(writer)
		if _, err = qp.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err = qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := parts.Close(); err != nil {
		return nil, err
	}
	headers := "From: " + c.From + "\r\nTo: " + strings.Join(c.To, ", ") + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=\"" + parts.Boundary() + "\"\r\n\r\n"
	return append([]byte(headers), content.Bytes()...), nil
}
