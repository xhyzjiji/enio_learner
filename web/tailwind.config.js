/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        panel: '#161719',
        surface: '#1d1e21',
        edge: '#2a2c30',
      },
    },
  },
  plugins: [],
}
