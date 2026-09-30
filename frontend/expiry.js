// Certificate display levels are independent of email notification settings.
export function certificateExpiry(expiresAt, now = Math.floor(Date.now() / 1000)) {
 const remaining = expiresAt - now;
 const level = remaining <= 0 ? 'expired' : remaining <= 86400 ? 'critical' : remaining <= 7 * 86400 ? 'warning' : remaining <= 30 * 86400 ? 'notice' : 'safe';
 const labels = {expired:'已过期',critical:'紧急',warning:'警告',notice:'提醒',safe:'有效期充足'};
 const seconds = Math.abs(remaining), days = Math.floor(seconds / 86400), hours = Math.floor(seconds % 86400 / 3600);
 const duration = days ? `${days} 天${hours ? ` ${hours} 小时` : ''}` : hours ? `${hours} 小时` : '不到 1 小时';
 return {level, remaining, label:labels[level], text:remaining <= 0 ? `已过期 ${duration}` : `距离到期还剩 ${duration}`};
}
