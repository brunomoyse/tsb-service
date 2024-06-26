<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsToMany;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Database\Eloquent\Relations\HasOne;

/**
 * Class Product
 *
 * @property string $id
 * @property string $created_at
 * @property string $updated_at
 * @property float $price
 * @property bool $is_active
 * @property string|null $code
 * @property string $slug
 * @property Attachment[] $attachments
 * @property ProductCategory[] $productCategories
 * @property ProductTranslation[] $productTranslations
 * @property Order[] $orders
 */
class Product extends Model
{
    use HasUuids;

    protected $table = 'products';

    protected $primaryKey = 'id';

    public $incrementing = false;

    protected $keyType = 'string';

    protected $fillable = [
        'price',
        'is_active',
        'code',
        'slug',
    ];

    public function preview(): HasOne
    {
        return $this->hasOne(Attachment::class)->where('preview', '=', true);
    }

    public function attachments(): HasMany
    {
        return $this->hasMany(Attachment::class);
    }

    public function productCategories(): BelongsToMany
    {
        return $this->belongsToMany(ProductCategory::class);
    }

    public function productTranslations(): HasMany
    {
        return $this->hasMany(ProductTranslation::class);
    }

    public function orders(): BelongsToMany
    {
        return $this->belongsToMany(Order::class)->withPivot('quantity');
    }
}
