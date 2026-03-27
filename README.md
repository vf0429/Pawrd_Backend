# PetWell 后端 (Go)

PetWell iOS 应用的 Go 语言后端服务。

## �️ 项目开发规范

为了保持项目的整洁和一致性，请遵循以下开发规范：

1.  **后端语言**: 必须使用 **Go** (Golang) 进行所有后端 API 和服务逻辑的开发。
2.  **数据处理**: 对于复杂的数据处理、ETL 任务或脚本生成，建议使用 **Python** (或其他合适的脚本语言)。
    - 例如：`scripts/build_insurance_db.py` 用于构建数据库。
3.  **数据库**: 使用 SQLite 存储数据。

---

## 🚀 快速开始

### 前置要求
- 安装 Go 1.21+
- SQLite (用于数据库)
- Python 3.x (用于数据处理脚本)

### 安装依赖
```bash
go mod tidy
```

### 导入保险数据
可以使用 Python 脚本来构建最新的保险数据库：
```bash
python3 scripts/build_insurance_db.py
```
或者使用 Go 遗留命令（如果仍在使用）：
```bash
go run cmd/import_data/main.go
```
这将从项目目录中的 CSV 文件导入数据并生成/更新 `insurance.db`。

### 启动服务器
```bash
go run main.go
```
服务器将在 `http://localhost:8000` 启动。

---

## 📡 API 端点

| 端点 (Endpoint)       | 方法   | 描述                                |
|-----------------------|--------|-------------------------------------|
| `/vaccines`           | GET    | 返回疫苗列表 (JSON)                 |
| `/clinics`            | GET    | 返回所有诊所列表 (JSON)             |
| `/emergency-clinics`  | GET    | 返回 24 小时急诊诊所                |
| `/register`           | POST   | 用户注册 (内存存储)                 |
| `/posts`              | GET/POST | 博客文章 (内存存储)                 |

### 测试端点
```bash
curl http://localhost:8000/vaccines
curl http://localhost:8000/clinics
curl http://localhost:8000/emergency-clinics
```

---

## 📁 数据文件

| 文件名               | 描述                               |
|--------------------|------------------------------------|
| `vaccines.json`    | 疫苗信息                           |
| `clinics.csv`      | 兽医诊所列表                       |
| `insurance.db`     | SQLite 数据库 (包含保险数据)        |
| `petwell.db`       | SQLite 数据库 (自动创建，用于其他数据)|

---

## 🗄️ 数据库架构 (Database Schema)

### `pet_insurance_comparison`
存储保险计划的主要信息。
- `insurance_provider`, `provider_key` (唯一标识), `category`, `subcategory`
- `coverage_percentage`, `cancer_cash` (癌症津贴), `cancer_cash_notes`, `additional_critical_cash_benefit`

### `coverage_limits`
存储各保险计划的具体赔偿限额。
- `limit_item`, `provider_key` (外键), `category`, `subcategory`, `level`
- `coverage_amount_hkd` (港币限额), `notes` (备注)

