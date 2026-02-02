const statusDiv = document.getElementById('status');
const activityTbody = document.querySelector('#activity-table tbody');
const statsTbody = document.querySelector('#stats-table tbody');
const nodesTbody = document.querySelector('#nodes-table tbody');
const logsContainer = document.getElementById('logs-container');
const logsContent = document.getElementById('logs-content');

function formatLastCheck(isoString) {
	const d = new Date(isoString);
	const mon = d.toLocaleString('en-US', { month: 'short' });
	const day = String(d.getDate()).padStart(2, '0');
	const h = String(d.getHours()).padStart(2, '0');
	const m = String(d.getMinutes()).padStart(2, '0');
	const s = String(d.getSeconds()).padStart(2, '0');
	return mon + '-' + day + ' ' + h + ':' + m + ':' + s;
}

function toggleTheme() {
	const current = document.documentElement.getAttribute('data-theme');
	const next = current === 'dark' ? 'light' : 'dark';
	document.documentElement.setAttribute('data-theme', next);
	localStorage.setItem('theme', next);
}

window.toggleTheme = toggleTheme;

const savedTheme = localStorage.getItem('theme') || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
document.documentElement.setAttribute('data-theme', savedTheme);

function connect() {
	const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
	const ws = new WebSocket(proto + '//' + window.location.host + '/ws');

	ws.onopen = function() {
		statusDiv.innerHTML = 'Connected';
		statusDiv.style.color = 'var(--success)';
	};

	ws.onmessage = function(event) {
		try {
			const data = JSON.parse(event.data);
			
			if (data.type === 'log') {
				const entry = document.createElement('div');
				entry.className = 'log-entry log-' + data.level.toLowerCase();
				
				entry.innerHTML = 
					'<span class="log-time">[' + data.timestamp + ']</span>' +
					'<span class="log-level">[' + data.level + ']</span> ' +
					'<span class="log-comp">[' + data.component + ']</span> ' +
					'<span class="log-msg">' + data.message + '</span>';
				
				logsContent.prepend(entry);
				
				if (logsContent.children.length > 200) {
					logsContent.lastChild.remove();
				}
				return;
			}
			
			if (data.type === 'config') {
				if (!data.hide_logs) {
					logsContainer.style.display = 'block';
				} else {
					logsContainer.style.display = 'none';
				}
				return;
			}

			render(data);
		} catch(e) {
			console.error('Parse error:', e);
		}
	};

	ws.onclose = function() {
		statusDiv.innerHTML = 'Disconnected. Reconnecting...';
		statusDiv.style.color = 'var(--error)';
		setTimeout(connect, 3000);
	};

	ws.onerror = function(err) {
		console.error('WS Error:', err);
		ws.close();
	};
}

function drawActivity(canvasId, windowData) {
	const canvas = document.getElementById(canvasId);
	if (!canvas) return;
	
	const dpr = window.devicePixelRatio || 1;
	const rect = canvas.getBoundingClientRect();
	
	canvas.width = rect.width * dpr;
	canvas.height = rect.height * dpr;
	
	const ctx = canvas.getContext('2d');
	ctx.scale(dpr, dpr);
	
	const width = rect.width;
	const height = rect.height;
	
	// Get only latest 100 blocks and reverse (newest on left)
	const MAX_BLOCKS = 100;
	let displayData = windowData;
	if (windowData.length > MAX_BLOCKS) {
		displayData = windowData.slice(-MAX_BLOCKS);
	}
	displayData = displayData.slice().reverse();
	
	const totalBlocks = displayData.length;
	if (totalBlocks === 0) return;

	ctx.clearRect(0, 0, width, height);

	const GAP = 1;
	const cellCount = totalBlocks;
	
	const totalGapWidth = GAP * (cellCount - 1);
	const availableWidth = width - totalGapWidth;
	const cellWidth = availableWidth / cellCount;

	for (let i = 0; i < cellCount; i++) {
		const x = i * (cellWidth + GAP);
		
		if (displayData[i]) {
			ctx.fillStyle = getComputedStyle(document.body).getPropertyValue('--success-dim');
		} else {
			ctx.fillStyle = getComputedStyle(document.body).getPropertyValue('--error');
		}

		ctx.fillRect(x, 0, cellWidth, height);
	}
}

function render(data) {
	// Update average block time in all badges
	if (data.avg_block_time && data.avg_block_time > 0) {
		const avgSec = (data.avg_block_time / 1000).toFixed(2) + "s";
		document.querySelectorAll(".block-time-value").forEach(el => {
			el.textContent = avgSec;
		});
	}

	const updateTable = (tbody, items, idField, createRow, updateRow) => {
		const existingRows = new Map();
		Array.from(tbody.children).forEach(tr => existingRows.set(tr.getAttribute('data-id'), tr));
		
		const newIds = new Set();
		
		items.forEach(item => {
			const id = item[idField];
			newIds.add(id);
			
			let tr = existingRows.get(id);
			if (!tr) {
				tr = createRow(item);
				tr.setAttribute('data-id', id);
				tbody.appendChild(tr);
			} else {
				updateRow(tr, item);
			}
		});

		existingRows.forEach((tr, id) => {
			if (!newIds.has(id)) {
				tr.remove();
			}
		});
	};

	if (data.validators) {
		updateTable(
			activityTbody,
			data.validators,
			'id',
			(v) => {
				const tr = document.createElement('tr');
				if (v.down) tr.className = 'down';
				const canvasId = 'vis-' + v.id;
				tr.innerHTML = 
					'<td><strong>'+v.moniker+'</strong></td>' +
					'<td>'+(v.uptime*100).toFixed(1)+'%</td>' +
					'<td><canvas id="'+canvasId+'" class="vis-canvas"></canvas></td>';
				
				if (v.window) {
					requestAnimationFrame(() => drawActivity(canvasId, v.window));
				}
				return tr;
			},
			(tr, v) => {
				if (v.down) {
					if (!tr.classList.contains('down')) tr.className = 'down';
				} else {
					tr.className = '';
				}
				const cells = tr.querySelectorAll('td');
				if (cells.length >= 2) {
					cells[1].textContent = (v.uptime*100).toFixed(1)+'%';
				}
				const canvasId = 'vis-' + v.id;
				if (v.window) {
					requestAnimationFrame(() => drawActivity(canvasId, v.window));
				}
			}
		);

		updateTable(
			statsTbody,
			data.validators,
			'id',
			(v) => {
				const tr = document.createElement('tr');
				if (v.down) tr.className = 'down';
				tr.innerHTML = 
					'<td><strong>'+v.moniker+'</strong></td>' +
					'<td>'+v.status+'</td>' +
					'<td>'+(v.uptime*100).toFixed(1)+'%</td>' +
					'<td>'+v.missed+'/'+v.total+'</td>' +
					'<td>'+v.staking+'</td>' +
					'<td>'+v.last_height+'</td>' +
					'<td>'+(v.down?'YES':'NO')+'</td>';
				return tr;
			},
			(tr, v) => {
				if (v.down) {
					if (!tr.classList.contains('down')) tr.className = 'down';
				} else {
					tr.className = '';
				}
				tr.innerHTML = 
					'<td><strong>'+v.moniker+'</strong></td>' +
					'<td>'+v.status+'</td>' +
					'<td>'+(v.uptime*100).toFixed(1)+'%</td>' +
					'<td>'+v.missed+'/'+v.total+'</td>' +
					'<td>'+v.staking+'</td>' +
					'<td>'+v.last_height+'</td>' +
					'<td>'+(v.down?'YES':'NO')+'</td>';
			}
		);
	}

	if (data.nodes) {
		updateTable(
			nodesTbody,
			data.nodes,
			'rpc_url',
			(n) => {
				const tr = document.createElement('tr');
				if (!n.healthy) tr.className = 'down';
				tr.innerHTML = 
					'<td>'+n.label+'</td>' +
					'<td>'+(n.healthy?'YES':'NO')+'</td>' +
					'<td>'+n.block_height+'</td>' +
					'<td>'+(n.syncing?'YES':'NO')+'</td>' +
					'<td>'+formatLastCheck(n.last_check)+'</td>';
				return tr;
			},
			(tr, n) => {
				if (!n.healthy) {
					if (!tr.classList.contains('down')) tr.className = 'down';
				} else {
					tr.className = '';
				}
				tr.innerHTML = 
					'<td>'+n.label+'</td>' +
					'<td>'+(n.healthy?'YES':'NO')+'</td>' +
					'<td>'+n.block_height+'</td>' +
					'<td>'+(n.syncing?'YES':'NO')+'</td>' +
					'<td>'+formatLastCheck(n.last_check)+'</td>';
			}
		);
	}
}

connect();
