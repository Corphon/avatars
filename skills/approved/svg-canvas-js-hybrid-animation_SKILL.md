---
name: SVG + Canvas + CSS + JS 混合动画开发
description: 通用型 Web 动画开发技能，结合 SVG 矢量场景、Canvas 动态渲染与 CSS 过渡/排版，适用于物理模拟、数据可视化、游戏动画与短提示词场景化动画生成，可使用P5.js(<script src="https://cdnjs.cloudflare.com/ajax/libs/p5.js/1.9.0/p5.min.js"></script>)
when_to_use: When the user requests web animation, SVG, Canvas, or visual effects generation.
version: 1.3.0
lifecycle-state: approved
approved-at: 2026-06-29T03:54:55Z
# Avatar Skill Prompt: SVG + Canvas + CSS + JS Hybrid Animation Generation
**Default expansion template** (use this as a mental model): 
**Phase‑driven CSS hook** (JS sets the phase, CSS reacts): 
- Position layers with `position: absolute`, `inset: 0`, `overflow: hidden`.
- Prefer **P5.js** via CDN (`<script src="https: //cdnjs.cloudflare.com/ajax/libs/p5.js/1.9.0/p5.min.js"></script>`) when the user explicitly requests it, or for quick prototyping of visual ideas, but keep the hybrid architecture intact.
- Use **Canvas** for high‑speed dynamic content: particles, trails, and real‑time effects.
--accent: #ffd86f;
--glow: rgba(255, 216, 111, 0.8);
--hud-opacity: 0.72;
All layers are stacked inside a relative container, `svg` and `canvas` elements with `position: absolute`. The overlay canvas must have `pointer-events: none` so that SVG interaction is not blocked.
Essential helpers: 
Every animation you create must: 
Expand the short prompt into a structured five‑part scenario: **character + action + scene + timeline + termination condition**.
Key classes: 
When asked to produce an animation, output a self‑contained HTML file that: 
When given a short description (e.g., "a basketball shot"), follow this translation pipeline before writing any code: 
author: AI Assistant
depth: y
id: svg-canvas-css-js-hybrid-animation
opacity: 1;
prompt: |
scale: scale,
tags: [animation, svg, canvas, css, javascript, physics-simulation, data-visualization, web-graphics, performance]
this.config = { startDelay: 1000, loopInterval: 5000, randomize: true, pauseOnInteraction: true, ...options };
this.renderers.push({ render: renderFn, priority });
this.systems.push({ update: updateFn, priority });
transform: translateY(0);
type: skill
x: centerX + x * scale,
y: centerY - z * scale,  // Y goes upward in world space
│  (z‑index: 0)                       │
│  Layer 0: SVG Base (static scene)   │  ← field, buildings, base shapes
│  Layer 1: Canvas Background (pre‑render)│ ← grids, heatmaps, static data viz
│  Layer 2: SVG Interactive (links)   │  ← clickable elements, controls
│  Layer 3: Canvas Overlay (dynamic)  │  ← particles, trails, real‑time data
│  pointer‑events: auto               │
---

*Design Philosophy: SVG holds the structure, Canvas delivers the dynamics, JavaScript owns the logic. They collaborate through a unified state and coordinate system to create performant, maintainable web animations.*

  ## Example: Jordan Slam Dunk with Crowd Cheer

  ```html
  <!DOCTYPE html>
  <html lang="en">
  <head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Jordan Slam Dunk</title>
    <style>
      * { margin: 0; padding: 0; box-sizing: border-box; }
      body {
        background: #0a0a0a;
        display: flex;
        justify-content: center;
        align-items: center;
        height: 100vh;
        overflow: hidden;
      }
      .scene-container {
        position: relative;
        width: 100vw;
        height: 100vh;
        max-width: 1200px;
        max-height: 700px;
        overflow: hidden;
        --accent: #ff4500;
        --crowd-energy: 0;
      }
      .scene-container[data-phase="celebrate"] {
        --crowd-energy: 1;
      }
      svg, canvas {
        position: absolute;
        top: 0;
        left: 0;
        width: 100%;
        height: 100%;
      }
      canvas {
        pointer-events: none;
      }
      .score-overlay {
        position: absolute;
        bottom: 20px;
        left: 50%;
        transform: translateX(-50%);
        font-family: 'Impact', Arial Black, sans-serif;
        font-size: 3rem;
        color: #ffd700;
        text-shadow: 0 0 20px rgba(255,215,0,0.8);
        z-index: 10;
        opacity: 0;
        transition: opacity 0.5s;
        pointer-events: none;
      }
      .scene-container[data-phase="celebrate"] .score-overlay {
        opacity: 1;
      }
    </style>
  </head>
  <body>
  <div class="scene-container" id="scene">
    <!-- SVG Base Layer -->
    <svg id="base-svg" viewBox="0 0 1200 700" preserveAspectRatio="xMidYMid meet">
      <!-- Court floor -->
      <rect x="0" y="500" width="1200" height="200" fill="#b87333" />
      <line x1="60" y1="500" x2="1140" y2="500" stroke="#fff" stroke-width="3" />
      
      <!-- Backboard and hoop -->
      <rect x="950" y="200" width="40" height="120" fill="#ddd" stroke="#333" stroke-width="2" rx="5" />
      <circle cx="970" cy="260" r="30" fill="none" stroke="#e74c3c" stroke-width="4" />
      <!-- Net -->
      <g id="net-group">
        <polyline points="955,290 970,290 970,320 955,290" fill="none" stroke="#ffffff" stroke-width="2" opacity="0.9" />
        <polyline points="985,290 970,290 970,320 985,290" fill="none" stroke="#ffffff" stroke-width="2" opacity="0.9" />
      </g>
      
      <!-- Crowd (multiple silhouetted figures) -->
      <g id="crowd">
        <!-- Heads -->
        <circle cx="50" cy="470" r="12" fill="#333" />
        <circle cx="100" cy="465" r="13" fill="#222" />
        <circle cx="150" cy="472" r="11" fill="#444" />
        <circle cx="200" cy="468" r="12" fill="#333" />
        <circle cx="250" cy="470" r="12" fill="#222" />
        <circle cx="300" cy="466" r="13" fill="#444" />
        <circle cx="350" cy="472" r="11" fill="#333" />
        <circle cx="400" cy="469" r="12" fill="#222" />
      </g>
      
      <!-- Player (Jordan) – initial position -->
      <g id="player">
        <!-- Body -->
        <rect x="180" y="360" width="20" height="50" fill="#ee3124" rx="5" />
        <!-- Head -->
        <circle cx="190" cy="350" r="15" fill="#2c2c2c" />
        <!-- Arms -->
        <line x1="180" y1="375" x2="150" y2="390" stroke="#2c2c2c" stroke-width="6" stroke-linecap="round" />
        <line x1="200" y1="375" x2="240" y2="380" stroke="#2c2c2c" stroke-width="6" stroke-linecap="round" />
        <!-- Legs -->
        <line x1="185" y1="410" x2="170" y2="450" stroke="#2c2c2c" stroke-width="7" stroke-linecap="round" />
        <line x1="195" y1="410" x2="210" y2="450" stroke="#2c2c2c" stroke-width="7" stroke-linecap="round" />
      </g>
      
      <!-- Ball placeholder (will be drawn on canvas) but keep position reference -->
    </svg>
    
    <canvas id="overlay-canvas"></canvas>
    
    <div class="score-overlay">SLAM!</div>
  </div>

  <script>
    // ===== Responsive Canvas =====
    class ResponsiveCanvas {
      constructor(canvas, width, height) {
        this.canvas = canvas;
        this.width = width;
        this.height = height;
        this.ctx = canvas.getContext('2d');
        this.resize();
        window.addEventListener('resize', this.resize.bind(this));
      }
      resize() {
        const rect = this.canvas.getBoundingClientRect();
        this.canvas.width = rect.width * window.devicePixelRatio;
        this.canvas.height = rect.height * window.devicePixelRatio;
        this.ctx.scale(window.devicePixelRatio, window.devicePixelRatio);
        this.canvas.style.width = rect.width + 'px';
        this.canvas.style.height = rect.height + 'px';
      }
      get ctx() { return this.canvas.getContext('2d'); }
    }

    // ===== PhysicsBody =====
    class PhysicsBody {
      constructor(x, y, mass = 1) {
        this.pos = { x, y };
        this.vel = { x: 0, y: 0 };
        this.mass = mass;
        this.invMass = 1 / mass;
        this.forces = { x: 0, y: 0 };
        this.drag = 0.001;
        this.restitution = 0.6;
      }
      applyForce(fx, fy) { this.forces.x += fx; this.forces.y += fy; }
      applyImpulse(ix, iy) { this.vel.x += ix * this.invMass; this.vel.y += iy * this.invMass; }
      update(dt) {
        const fx = this.forces.x - this.drag * this.vel.x;
        const fy = this.forces.y - this.drag * this.vel.y;
        this.vel.x += fx * this.invMass * dt;
        this.vel.y += fy * this.invMass * dt;
        this.pos.x += this.vel.x * dt;
        this.pos.y += this.vel.y * dt;
        this.forces.x = 0; this.forces.y = 0;
      }
    }

    // ===== CollisionSystem (ground only) =====
    class CollisionSystem {
      constructor() { this.bodies = []; this.groundY = 500; }
      add(body) { this.bodies.push(body); }
      update() {
        for (let body of this.bodies) {
          if (body.pos.y > this.groundY) {
            body.pos.y = this.groundY;
            body.vel.y *= -body.restitution;
            if (Math.abs(body.vel.y) < 1) body.vel.y = 0;
          }
        }
      }
    }

    // ===== TrailRenderer =====
    class TrailRenderer {
      constructor(maxPoints = 20) { this.positions = []; this.maxPoints = maxPoints; }
      add(pos) {
        this.positions.push({ x: pos.x, y: pos.y });
        if (this.positions.length > this.maxPoints) this.positions.shift();
      }
      draw(ctx) {
        if (this.positions.length < 2) return;
        ctx.beginPath();
        ctx.moveTo(this.positions[0].x, this.positions[0].y);
        for (let i = 1; i < this.positions.length; i++) {
          ctx.lineTo(this.positions[i].x, this.positions[i].y);
        }
        ctx.strokeStyle = 'rgba(255,140,0,0.7)';
        ctx.lineWidth = 3;
        ctx.stroke();
      }
    }

    // ===== ParticleSystem =====
    class ParticleSystem {
      constructor() { this.particles = []; }
      emit(x, y, count, config) {
        for (let i = 0; i < count; i++) {
          this.particles.push({
            x, y,
            vx: (Math.random() - 0.5) * config.speed,
            vy: (Math.random() - 0.5) * config.speed - Math.random() * 150,
            life: 1,
            decay: 1 / config.lifetime,
            size: config.size || 4,
            color: config.color || '#ff0'
          });
        }
      }
      update(dt) {
        this.particles = this.particles.filter(p => p.life > 0);
        for (let p of this.particles) {
          p.x += p.vx * dt;
          p.y += p.vy * dt;
          p.life -= p.decay * dt;
        }
      }
      draw(ctx) {
        for (let p of this.particles) {
          ctx.globalAlpha = p.life;
          ctx.fillStyle = p.color;
          ctx.beginPath();
          ctx.arc(p.x, p.y, p.size, 0, Math.PI * 2);
          ctx.fill();
        }
        ctx.globalAlpha = 1;
      }
    }

    // ===== AutoPlayController =====
    class AutoPlayController {
      constructor(options = {}) {
        this.config = { startDelay: 500, loopInterval: 7000, randomize: false, pauseOnInteraction: false, ...options };
        this.isPlaying = false;
        this.timer = null;
      }
      start(onPlay) {
        this.isPlaying = true;
        clearTimeout(this.timer);
        this.timer = setTimeout(() => {
          const loop = () => {
            onPlay();
            if (this.isPlaying) {
              this.timer = setTimeout(loop, this.config.loopInterval);
            }
          };
          loop();
        }, this.config.startDelay);
      }
      pause() { this.isPlaying = false; clearTimeout(this.timer); }
      resume(onPlay) { this.start(onPlay); }
      destroy() { this.pause(); }
    }

    // ===== Scene Initialization =====
    (function() {
      const scene = document.getElementById('scene');
      const svg = document.getElementById('base-svg');
      const canvas = document.getElementById('overlay-canvas');
      const responsiveCanvas = new ResponsiveCanvas(canvas, 1200, 700);
      const ctx = responsiveCanvas.ctx;
      
      // Coordinate system (SVG viewBox)
      const coord = new CoordinateSystem(svg, canvas);
      
      const engine = new AnimationEngine({ physicsStep: 1000/60 });
      
      // Physics for ball
      const ball = new PhysicsBody(200, 350, 0.6);
      ball.restitution = 0.5;
      const collisionSystem = new CollisionSystem();
      collisionSystem.add(ball);
      
      // Trail
      const trail = new TrailRenderer(15);
      
      // Particles
      const particleSystem = new ParticleSystem();
      
      // Player state
      const player = {
        x: 200, y: 450, // base position (feet)
        jumpOffset: 0,
        scale: 1,
        dunkAnim: 0
      };
      
      // Timeline phases
      const timeline = new SceneTimeline([
        { // Phase 1: Approach
          duration: 2.0,
          enter: () => {
            scene.dataset.phase = 'approach';
            ball.pos.x = 200; ball.pos.y = 350;
            ball.vel.x = 0; ball.vel.y = 0;
            trail.positions = [];
            particleSystem.particles = [];
          },
          update: (dt, t) => {
            player.x = 200 + (850 - 200) * Math.min(t / 2.0, 1);
            player.y = 450;
            ball.pos.x = player.x + 10;
            ball.pos.y = player.y - 80;
          }
        },
        { // Phase 2: Jump and release
          duration: 0.6,
          enter: () => {
            scene.dataset.phase = 'jump';
          },
          update: (dt, t) => {
            const progress = t / 0.6;
            player.y = 450 - 100 * Math.sin(progress * Math.PI);
            player.jumpOffset = -100 * Math.sin(progress * Math.PI);
            ball.pos.x = player.x + 20;
            ball.pos.y = player.y - 60;
            if (t > 0.3) {
              // launch ball
              if (ball.vel.x === 0 && ball.vel.y === 0) {
                ball.pos.x = player.x + 40;
                ball.pos.y = player.y - 50;
                ball.vel.x = 350;
                ball.vel.y = -300;
              }
            }
          }
        },
        { // Phase 3: Ball flight & dunk
          duration: 0.8,
          enter: () => {
            scene.dataset.phase = 'flight';
          },
          update: (dt, t) => {
            // let physics take over
            ball.applyForce(0, 600);
          }
        },
        { // Phase 4: Celebrate
          duration: 2.0,
          enter: () => {
            scene.dataset.phase = 'celebrate';
            particleSystem.emit(970, 260, 80, {
              speed: 200,
              lifetime: 1.5,
              size: 5,
              color: '#ffd700'
            });
            // confetti
            for (let i = 0; i < 60; i++) {
              particleSystem.particles.push({
                x: 970, y: 260,
                vx: (Math.random() - 0.5) * 300,
                vy: -Math.random() * 400,
                life: 1,
                decay: 0.5,
                size: 6,
                color: ['#ff4500','#ffd700','#ff1493','#00ff00'][Math.floor(Math.random()*4)]
              });
            }
          },
          update: (dt, t) => {
            ball.applyForce(0, 600);
          }
        }
      ]);
      
      // Reset function
      function resetAndPlay() {
        timeline.index = 0;
        timeline.timeInPhase = 0;
        timeline.finished = false;
        player.x = 200;
        player.y = 450;
        ball.pos.x = 200;
        ball.pos.y = 350;
        ball.vel.x = 0;
        ball.vel.y = 0;
      }
      
      // Register systems
      engine.registerSystem((dt) => {
        timeline.update(dt);
        if (timeline.finished) {
          engine.stop();
          autoPlay.start(resetAndPlay);
        }
        ball.update(dt);
        collisionSystem.update();
        trail.add({ x: ball.pos.x, y: ball.pos.y });
        particleSystem.update(dt);
      }, 0);
      
      engine.registerRenderer((alpha) => {
        ctx.clearRect(0, 0, canvas.width / window.devicePixelRatio, canvas.height / window.devicePixelRatio);
        // Draw trail
        trail.draw(ctx);
        // Draw ball (3D-ish)
        const grad = ctx.createRadialGradient(ball.pos.x-4, ball.pos.y-4, 2, ball.pos.x, ball.pos.y, 12);
        grad.addColorStop(0, '#ff7b00');
        grad.addColorStop(1, '#8b4500');
        ctx.beginPath();
        ctx.arc(ball.pos.x, ball.pos.y, 12, 0, Math.PI*2);
        ctx.fillStyle = grad;
        ctx.fill();
        ctx.strokeStyle = '#000';
        ctx.lineWidth = 2;
        ctx.stroke();
        // Seams
        ctx.beginPath();
        ctx.arc(ball.pos.x, ball.pos.y, 12, 0, Math.PI);
        ctx.strokeStyle = '#000';
        ctx.lineWidth = 1;
        ctx.stroke();
        
        // Draw particles
        particleSystem.draw(ctx);
        
        // Update SVG player position
        const playerGroup = document.getElementById('player');
        if (playerGroup) {
          playerGroup.setAttribute('transform', `translate(${player.x - 200}, ${player.y - 450})`);
        }
        
        // Net stretch effect on celebrate
        if (scene.dataset.phase === 'celebrate') {
          const net = document.getElementById('net-group');
          if (net) net.setAttribute('transform', 'translate(0, 5)');
        } else {
          const net = document.getElementById('net-group');
          if (net) net.setAttribute('transform', '');
        }
        
        // Crowd energy
        const crowd = document.getElementById('crowd');
        if (crowd) {
          const energy = scene.dataset.phase === 'celebrate' ? 1 : 0;
          crowd.setAttribute('transform', `scale(${1 + energy * 0.1})`);
        }
      });
      
      // AutoPlay
      const autoPlay = new AutoPlayController({ loopInterval: 7000 });
      autoPlay.start(() => {
        resetAndPlay();
        engine.stop();
        engine.start();
      });
      
      // Initial start
      resetAndPlay();
      engine.start();
    })();
  </script>
  </body>
  </html>
  ```
---
<!-- Skill prompt is defined in YAML front matter -->
